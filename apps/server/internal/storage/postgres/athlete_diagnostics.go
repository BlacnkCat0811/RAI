package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	am "rai/server/internal/athletemodel"
	diag "rai/server/internal/diagnostics"
	"rai/server/internal/modelmanifest"
	pf "rai/server/internal/performance"
	"rai/server/internal/syncing"
	tl "rai/server/internal/trainingload"
)

var researchProductKeys = []string{
	"running_ability_maximal_v2",
	"workout_durability",
	"aerobic_efficiency",
	"critical_speed",
	"load_settling",
}

var researchProductFamilies = []string{
	"model_research_v1/running_ability_maximal_v2",
	"model_research_v1/workout_durability",
	"model_research_v1/aerobic_efficiency",
	"model_research_v1/critical_speed",
	"model_research_v1/load_settling",
}

// storedResearchEnvelope mirrors researchEnvelope with the result typed so a
// re-marshal reproduces the published content hash. The payload column is
// jsonb, which normalises object key order, so hashing raw result bytes would
// not match the original Go encoding.
type storedResearchEnvelope[T any] struct {
	SchemaVersion string            `json:"schemaVersion"`
	DatasetID     string            `json:"datasetID"`
	Generation    int64             `json:"generation"`
	Identity      diag.Identity     `json:"identity"`
	Scope         string            `json:"scope"`
	AsOf          int64             `json:"asOfUs"`
	Status        diag.Status       `json:"status"`
	Reasons       []diag.ReasonCode `json:"reasonCodes"`
	Result        *T                `json:"result,omitempty"`
}

func decodeResearchEnvelope[T any](payload []byte) (*storedResearchEnvelope[T], error) {
	var env storedResearchEnvelope[T]
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

type researchEnvelopeView interface {
	metadata() (schema, dataset string, generation int64, identity diag.Identity, scope string, status diag.Status)
	hash() string
}

func (e *storedResearchEnvelope[T]) metadata() (string, string, int64, diag.Identity, string, diag.Status) {
	return e.SchemaVersion, e.DatasetID, e.Generation, e.Identity, e.Scope, e.Status
}

func (e *storedResearchEnvelope[T]) hash() string { return tl.Hash(e) }

func validResearchEnvelope(env researchEnvelopeView, contentHash, dataset, stack, key string, identity diag.Identity, generation int64) bool {
	schema, ds, gen, id, scope, status := env.metadata()
	return schema == identity.Schema && ds == dataset && gen == generation && id == identity &&
		scope == "model/"+stack+"/athlete/diagnostic/v1/"+key && researchStatusValid(status) &&
		env.hash() == contentHash
}

// ReadAthleteDiagnostics projects the active stack's research artifacts. It is
// a read-only projection: no model runs, raw reads or candidate fallbacks.
func (s *Store) ReadAthleteDiagnostics(ctx context.Context, p syncing.Principal, at time.Time) (diag.ProductSnapshot, error) {
	out := diag.ProductSnapshot{SchemaVersion: diag.ProductSchema, Experimental: true}
	if !syncing.ValidID(p.DatasetID) || at.IsZero() {
		return out, ErrModelProjection
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	snapshot, c, err := readModelContext(ctx, tx, p.DatasetID, "active", at)
	if err != nil {
		return out, err
	}
	stack := c.assignment.StackID
	if stack == "" || stack == "legacy" {
		return out, am.ErrNotActivated
	}
	out.AsOf = at.UnixMicro()
	out.CalendarDate = snapshot.CalendarDate
	out.Assignment = diag.ProductAssignment{Channel: "active", StackID: stack, Version: c.assignment.Version}
	out.Items = []diag.ProductItem{}
	if stack != modelmanifest.StackID() {
		return out, tx.Commit(ctx)
	}
	manifest, err := modelmanifest.CurrentHash()
	if err != nil || c.configHash != manifest {
		return diag.ProductSnapshot{}, ErrModelProjection
	}
	day := snapshot.CalendarDate
	prefix := "model/" + stack + "/athlete/day/v2/"
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (a.artifact_key) a.artifact_key,a.payload,encode(a.content_sha256,'hex'),
 a.schema_version,r.algorithm_name,r.algorithm_version,r.target_generation,p.published_at,p.day::text,p.metric_family
FROM analysis_publications p
JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.scope_key=p.scope_key AND r.target_generation=p.generation
JOIN analysis_published_research_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id AND a.artifact_key=ANY($4::text[])
JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key AND d.generation=p.generation AND d.clean_generation=p.generation
WHERE p.dataset_id=$1 AND left(p.scope_key,length($2))=$2 AND p.metric_family=ANY($3::text[])
 AND r.status='SUCCEEDED' AND p.day<=$5::date
ORDER BY a.artifact_key,p.day DESC,p.published_at DESC,p.published_run_id`, p.DatasetID, prefix, researchProductFamilies, researchProductKeys, day)
	if err != nil {
		return diag.ProductSnapshot{}, err
	}
	defer rows.Close()
	type researchRow struct {
		key, hash, schema, algorithm, version, day, family string
		payload                                            []byte
		generation                                         int64
		publishedAt                                        time.Time
	}
	found := map[string]researchRow{}
	for rows.Next() {
		var r researchRow
		if err = rows.Scan(&r.key, &r.payload, &r.hash, &r.schema, &r.algorithm, &r.version, &r.generation, &r.publishedAt, &r.day, &r.family); err != nil {
			return diag.ProductSnapshot{}, err
		}
		found[r.key] = r
	}
	if err = rows.Err(); err != nil {
		return diag.ProductSnapshot{}, err
	}
	rows.Close()
	for _, key := range researchProductKeys {
		r, ok := found[key]
		if !ok {
			continue
		}
		item, err := researchProductItem(key, r.family, r.schema, r.algorithm, r.version, r.hash, r.day, r.generation, r.publishedAt, r.payload, p.DatasetID, stack)
		if err != nil {
			return diag.ProductSnapshot{}, fmt.Errorf("research artifact %s: %w", key, err)
		}
		out.Items = append(out.Items, item)
	}
	if err = tx.Commit(ctx); err != nil {
		return diag.ProductSnapshot{}, err
	}
	return out, nil
}

func researchProductItem(key, family, schema, algorithm, version, contentHash, day string, generation int64, publishedAt time.Time, payload []byte, dataset, stack string) (diag.ProductItem, error) {
	item := diag.ProductItem{
		Key:          key,
		Schema:       schema,
		Version:      version,
		Experimental: true,
		ReasonCodes:  []diag.ReasonCode{},
		Support:      diag.Support{Level: diag.SupportNone},
		Day:          day,
		PublishedAt:  publishedAt.UnixMicro(),
	}
	if family != "model_research_v1/"+key {
		return diag.ProductItem{}, ErrModelProjection
	}
	if key == "running_ability_maximal_v2" {
		var r pf.Result
		if schema != pf.AbilitySchemaV2 || algorithm != pf.CurveModel || version != pf.VersionV2 ||
			json.Unmarshal(payload, &r) != nil || r.Schema != pf.AbilitySchemaV2 || r.Model != pf.AbilityModel ||
			r.Version != pf.VersionV2 || r.DatasetID != dataset || r.Generation != generation ||
			r.ConfigHash != pf.ConfigHashV2() || r.EvidenceMode != pf.ModeMaximalV2 ||
			pf.Hash(r) != contentHash {
			return diag.ProductItem{}, ErrModelProjection
		}
		item.Model, item.Version, item.Schema = pf.AbilityModel, pf.VersionV2, pf.AbilitySchemaV2
		item.Status = diag.ProductStatusOfPerformance(r.Status)
		item.AsOf = r.AsOf
		item.Support = diag.Support{Level: researchSupportLevel(r.Confidence), Fraction: researchFraction(r.Support.QualitySupport), Count: r.Support.IndependentWorkouts}
		for _, code := range r.ReasonCodes {
			item.ReasonCodes = append(item.ReasonCodes, diag.ReasonCode(code))
		}
		if r.Ability != nil {
			v60, gamma, retention, score := r.Ability.V60, r.Ability.Gamma, r.Ability.Retention, r.Ability.Score
			item.Maximal = &diag.MaximalProduct{Score: &score, V60: &v60, Gamma: &gamma, EnduranceRetention: &retention, Confidence: r.Confidence, AnchorDay: r.AnchorDay}
		}
		return item, nil
	}
	identity, ok := researchIdentity(key)
	if !ok {
		return diag.ProductItem{}, ErrModelProjection
	}
	if schema != identity.Schema || algorithm != identity.Model || version != identity.Version {
		return diag.ProductItem{}, ErrModelProjection
	}
	switch key {
	case "workout_durability":
		env, err := decodeResearchEnvelope[diag.DurabilityResult](payload)
		if err != nil || !validResearchEnvelope(env, contentHash, dataset, stack, key, identity, generation) {
			return diag.ProductItem{}, ErrModelProjection
		}
		fillResearchItem(&item, identity, env.AsOf, env.Status, env.Reasons)
		if env.Result == nil {
			return item, nil
		}
		r := env.Result
		if !researchHeaderValid(r.Header, identity) {
			return diag.ProductItem{}, ErrModelProjection
		}
		item.Support = r.Support
		item.Durability = &diag.DurabilityProduct{WorkoutID: r.WorkoutID, DurationSeconds: r.DurationSeconds, AnalyzedSeconds: r.AnalyzedSeconds, EF1: r.EF1, EF2: r.EF2, Decoupling: r.Decoupling, PaceDecay: r.PaceDecay}
	case "aerobic_efficiency":
		env, err := decodeResearchEnvelope[diag.AerobicEfficiencyResult](payload)
		if err != nil || !validResearchEnvelope(env, contentHash, dataset, stack, key, identity, generation) {
			return diag.ProductItem{}, ErrModelProjection
		}
		fillResearchItem(&item, identity, env.AsOf, env.Status, env.Reasons)
		if env.Result == nil {
			return item, nil
		}
		r := env.Result
		if !researchHeaderValid(r.Header, identity) {
			return diag.ProductItem{}, ErrModelProjection
		}
		item.Support = r.Support
		item.Efficiency = &diag.EfficiencyProduct{WorkoutID: r.WorkoutID, EfficiencyMPerBeat: r.EfficiencyMPerBeat, SpeedMPerMin: r.SpeedMPerMin, HeartRateBPM: r.HeartRateBPM, ValidSeconds: r.ValidSeconds}
	case "critical_speed":
		env, err := decodeResearchEnvelope[diag.CriticalSpeedResult](payload)
		if err != nil || !validResearchEnvelope(env, contentHash, dataset, stack, key, identity, generation) {
			return diag.ProductItem{}, ErrModelProjection
		}
		fillResearchItem(&item, identity, env.AsOf, env.Status, env.Reasons)
		if env.Result == nil {
			return item, nil
		}
		r := env.Result
		if !researchHeaderValid(r.Header, identity) {
			return diag.ProductItem{}, ErrModelProjection
		}
		item.Support = r.Support
		item.CriticalSpeed = &diag.CriticalSpeedProduct{CSMPS: r.CSMPS, DPrimeMeters: r.DPrimeMeters, R2: r.R2, WorkoutCount: r.WorkoutCount}
	case "load_settling":
		env, err := decodeResearchEnvelope[diag.SettlingResult](payload)
		if err != nil || !validResearchEnvelope(env, contentHash, dataset, stack, key, identity, generation) {
			return diag.ProductItem{}, ErrModelProjection
		}
		fillResearchItem(&item, identity, env.AsOf, env.Status, env.Reasons)
		if env.Result == nil {
			return item, nil
		}
		r := env.Result
		if !researchHeaderValid(r.Header, identity) {
			return diag.ProductItem{}, ErrModelProjection
		}
		item.Support = r.Support
		item.LoadSettling = &diag.SettlingProduct{Hours: r.Hours, Bucket: r.Bucket, Load: r.Load, Baseline: r.Baseline, Score: r.Score}
	}
	return item, nil
}

func fillResearchItem(item *diag.ProductItem, identity diag.Identity, at int64, status diag.Status, reasons []diag.ReasonCode) {
	item.Model, item.Version, item.Schema = identity.Model, identity.Version, identity.Schema
	item.Status = diag.ProductStatusOf(status)
	item.AsOf = at
	for _, code := range reasons {
		item.ReasonCodes = append(item.ReasonCodes, code)
	}
	sort.Slice(item.ReasonCodes, func(i, j int) bool { return item.ReasonCodes[i] < item.ReasonCodes[j] })
}

func researchIdentity(key string) (diag.Identity, bool) {
	switch key {
	case "workout_durability":
		return diag.WorkoutDurabilityIdentity(), true
	case "aerobic_efficiency":
		return diag.AerobicEfficiencyIdentity(), true
	case "critical_speed":
		return diag.CriticalSpeedIdentity(), true
	case "load_settling":
		return diag.LoadSettlingIdentity(), true
	}
	return diag.Identity{}, false
}

func researchHeaderValid(h diag.Header, identity diag.Identity) bool {
	return h.Identity == identity && researchStatusValid(h.Status)
}

func researchStatusValid(s diag.Status) bool {
	switch s {
	case diag.StatusOK, diag.StatusPartial, diag.StatusCollecting, diag.StatusInsufficient, diag.StatusUnstable:
		return true
	}
	return false
}

func researchSupportLevel(confidence float64) diag.SupportLevel {
	switch {
	case confidence >= 0.4:
		return diag.SupportHigh
	case confidence >= 0.2:
		return diag.SupportModerate
	case confidence > 0:
		return diag.SupportLow
	}
	return diag.SupportNone
}

func researchFraction(v float64) float64 {
	if v < 0 || v != v {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
