package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	am "rai/server/internal/athletemodel"
	pf "rai/server/internal/performance"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	"rai/server/internal/storage/postgres/dbgen"
	"rai/server/internal/syncing"
	tl "rai/server/internal/trainingload"
	ve "rai/server/internal/vo2maxexternal"
	wf "rai/server/internal/workoutfeatures"
)

var ErrModelProjection = errors.New("athlete_model_technical_failure")

type ModelSnapshotStats struct {
	DBReads   int   `json:"dbReads"`
	ReadNS    int64 `json:"readNS"`
	FreshenNS int64 `json:"freshenNS"`
	MappingNS int64 `json:"mappingNS"`
}
type modelReadContext struct {
	timezone, epoch, collector, configHash string
	revision                               int64
	assignment                             am.Assignment
	extensions                             map[string]json.RawMessage
	validSession                           bool
}

func readModelContext(ctx context.Context, tx pgx.Tx, dataset, channel string, at time.Time) (am.AthleteModelSnapshot, modelReadContext, error) {
	var c modelReadContext
	var raw []byte
	e := tx.QueryRow(ctx, `SELECT d.timezone,d.epoch::text,d.commit_rev,COALESCE(c.collector_epoch::text,''),COALESCE(m.stack_id,CASE WHEN $2='active' THEN 'legacy' ELSE '' END),COALESCE(m.assignment_version,0),COALESCE(m.extension_configs,'{}'::jsonb),COALESCE(encode(m.config_sha256,'hex'),''),NOT EXISTS(SELECT 1 FROM collector_leases l LEFT JOIN devices v ON v.dataset_id=l.dataset_id AND v.id=l.device_id WHERE l.dataset_id=d.id AND (v.id IS NULL OR NOT v.can_collect OR v.revoked_at IS NOT NULL)) FROM datasets d LEFT JOIN collector_leases c ON c.dataset_id=d.id LEFT JOIN analysis_model_channels m ON m.dataset_id=d.id AND m.channel=$2 WHERE d.id=$1 AND d.retired_at IS NULL`, dataset, channel).Scan(&c.timezone, &c.epoch, &c.revision, &c.collector, &c.assignment.StackID, &c.assignment.Version, &raw, &c.configHash, &c.validSession)
	if e != nil {
		return am.AthleteModelSnapshot{}, c, e
	}
	if json.Unmarshal(raw, &c.extensions) != nil {
		return am.AthleteModelSnapshot{}, c, ErrModelProjection
	}
	loc, e := time.LoadLocation(c.timezone)
	if e != nil {
		return am.AthleteModelSnapshot{}, c, ErrModelProjection
	}
	c.assignment.Channel = channel
	return am.NewSnapshot(at.UnixMicro(), c.revision, c.timezone, at.In(loc).Format("2006-01-02"), c.assignment), c, nil
}

// ReadAthleteModelSnapshot selects only the active assignment. Legacy metrics
// are deliberately not relabelled as new model semantics. Existing product APIs
// continue to return their frozen legacy DTOs; this service has no HTTP route.
func (s *Store) ReadAthleteModelSnapshot(ctx context.Context, p syncing.Principal, at time.Time) (am.AthleteModelSnapshot, error) {
	if !syncing.ValidID(p.DatasetID) || at.IsZero() {
		return am.AthleteModelSnapshot{}, ErrModelProjection
	}
	tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return am.AthleteModelSnapshot{}, e
	}
	defer tx.Rollback(ctx)
	out, c, e := readModelContext(ctx, tx, p.DatasetID, "active", at)
	if e != nil {
		return out, e
	}
	if c.assignment.StackID != "legacy" {
		return am.AthleteModelSnapshot{}, ErrModelProjection
	}
	return out, tx.Commit(ctx)
}

type modelSlot struct {
	Metric string `json:"metric"`
	Key    string `json:"key"`
	Scope  string `json:"scope"`
	Family string `json:"family"`
}
type projectionRef struct {
	Metric string `json:"metric"`
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Type   string `json:"type"`
	Scope  string `json:"scope"`
	Run    string `json:"run"`
	Key    string `json:"key"`
	Hash   string `json:"hash"`
}
type candidateModels struct {
	load      *tl.Result
	state     *tl.StateResult
	ability   *pf.Result
	vo2       *ve.Result
	recovery  *rc.Result
	readiness *rd.Result
}

// ReadAthleteModelCandidateSnapshot requires administrative DB authority. It
// cannot be called successfully by the restricted API or worker role. All
// reads, controls, dependency identities and assignments share one RR view.
func (s *Store) ReadAthleteModelCandidateSnapshot(ctx context.Context, dataset string, at time.Time) (am.AthleteModelCandidateSnapshot, ModelSnapshotStats, error) {
	stats := ModelSnapshotStats{}
	start := time.Now()
	if !syncing.ValidID(dataset) || at.IsZero() {
		return am.AthleteModelCandidateSnapshot{}, stats, ErrModelProjection
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return am.AthleteModelCandidateSnapshot{}, stats, e
	}
	defer tx.Rollback(ctx)
	var admin bool
	e = tx.QueryRow(ctx, `SELECT r.rolsuper OR pg_has_role(current_user,d.datdba,'MEMBER') FROM pg_roles r JOIN pg_database d ON d.datname=current_database() WHERE r.rolname=current_user`).Scan(&admin)
	stats.DBReads++
	if e != nil {
		return am.AthleteModelCandidateSnapshot{}, stats, e
	}
	if !admin {
		return am.AthleteModelCandidateSnapshot{}, stats, errors.New("administrative model preview required")
	}
	out, c, e := readModelContext(ctx, tx, dataset, "candidate", at)
	stats.DBReads++
	if e != nil {
		return am.AthleteModelCandidateSnapshot{}, stats, e
	}
	slots := []modelSlot{{"loadWorkout", tl.ArtifactKey, "model/" + wf.StackID() + "/workout_load/", "workout_load_candidate"}, {"loadState", tl.StateKey, tl.StateScope(), "load_state_candidate"}, {"ability", pf.ArtifactKey, pf.Scope(), "performance_candidate"}, {"vo2", ve.ArtifactKey, ve.Scope, ve.Family}, {"recovery", "physiological_recovery", rc.Scope(c.timezone, out.CalendarDate), rc.Family}, {"readiness", rd.Key, rd.Scope(c.timezone), rd.Family}}
	b, _ := json.Marshal(slots)
	d, _ := time.Parse("2006-01-02", out.CalendarDate)
	rows, e := dbgen.New(tx).AthleteModelCurrentArtifacts(ctx, dbgen.AthleteModelCurrentArtifactsParams{DatasetID: dataset, Column2: b, Column3: pgtype.Date{Time: d, Valid: true}, Column4: pgtype.Timestamptz{Time: at, Valid: true}})
	stats.DBReads++
	if e != nil {
		return am.AthleteModelCandidateSnapshot{}, stats, e
	}
	models := candidateModels{}
	metadata := map[string]dbgen.AthleteModelCurrentArtifactsRow{}
	fail := map[string]string{}
	refs := []projectionRef{}
	for _, row := range rows {
		metadata[row.Metric] = row
		if row.RunID == "" {
			model := map[string]string{"recovery": rc.Model, "readiness": rd.Model, "vo2": ve.Model, "ability": "rai.performance_curve/v1"}[row.Metric]
			if model != "" && c.extensions[model] != nil || row.Metric == "loadWorkout" && row.EventEndUs > 0 {
				fail[row.Metric] = "sync_incomplete"
			}
			continue
		}
		if !row.Clean {
			fail[row.Metric] = "sync_incomplete"
			continue
		}
		if row.RunStatus != "SUCCEEDED" || row.Generation != row.RunGeneration || len(row.Payload) == 0 {
			fail[row.Metric] = "technical_failure"
			continue
		}
		more, status := decodeProductArtifact(row, c, dataset, &models)
		if status != "" {
			fail[row.Metric] = status
			continue
		}
		refs = append(refs, more...)
	}
	if len(refs) > 20000 {
		return am.AthleteModelCandidateSnapshot{}, stats, ErrModelProjection
	}
	if len(refs) > 0 {
		b, _ = json.Marshal(refs)
		checks, err := dbgen.New(tx).AthleteModelValidateRefs(ctx, dbgen.AthleteModelValidateRefsParams{DatasetID: dataset, Column2: b})
		stats.DBReads++
		if err != nil {
			return am.AthleteModelCandidateSnapshot{}, stats, err
		}
		for _, v := range checks {
			if !v.HashOk {
				fail[v.Metric] = "technical_failure"
			} else if !v.Current {
				fail[v.Metric] = "sync_incomplete"
			}
		}
	}
	if models.vo2 != nil && fail["vo2"] == "" {
		selected, err := externalSelectionAt(ctx, tx, dataset, at)
		stats.DBReads++
		if err != nil {
			return am.AthleteModelCandidateSnapshot{}, stats, err
		}
		persisted := ""
		if models.vo2.SelectionID != nil {
			persisted = *models.vo2.SelectionID
		}
		if selected != persisted {
			fail["vo2"] = "source_selection_required"
		}
	}
	// A dependent value cannot remain present when a directly read upstream
	// artifact fails payload integrity (even if its stored hash was unchanged).
	if r := models.readiness; r != nil {
		if r.Recovery != nil && r.Recovery.RunID == metadata["recovery"].RunID && fail["recovery"] == "technical_failure" {
			fail["readiness"] = "technical_failure"
		}
		for _, ref := range r.Refs {
			if ref.RunID == metadata["loadWorkout"].RunID && fail["loadWorkout"] == "technical_failure" {
				fail["readiness"] = "technical_failure"
			}
		}
	}
	stats.ReadNS = time.Since(start).Nanoseconds()
	start = time.Now()
	// Freshen only: no fitting, baseline generation, raw reads or model Calculate.
	if r := models.ability; r != nil && fail["ability"] == "" {
		if at.UnixMicro() < r.AsOf {
			fail["ability"] = "technical_failure"
		} else {
			v := pf.Freshen(*r, at.UnixMicro())
			models.ability = &v
		}
	}
	if r := models.vo2; r != nil && fail["vo2"] == "" {
		v, err := ve.Freshen(*r, at.UnixMicro())
		if err != nil {
			fail["vo2"] = "technical_failure"
		} else {
			models.vo2 = &v
		}
	}
	if r := models.recovery; r != nil && fail["recovery"] == "" {
		v, err := rc.Freshen(*r, at.UnixMicro())
		if err != nil {
			fail["recovery"] = "technical_failure"
		} else {
			models.recovery = &v
		}
	}
	if r := models.readiness; r != nil && fail["readiness"] == "" {
		v, err := rd.Freshen(*r, at.UnixMicro())
		if err != nil {
			fail["readiness"] = "technical_failure"
		} else {
			models.readiness = &v
		}
	}
	stats.FreshenNS = time.Since(start).Nanoseconds()
	start = time.Now()
	if models.ability != nil && fail["ability"] == "" {
		r := metadata["ability"]
		var anchor struct {
			AsOf int64 `json:"asOfUs"`
		}
		_ = json.Unmarshal(r.Payload, &anchor)
		out.RunningAbility = am.ProjectAbility(*models.ability, anchor.AsOf, r.UpdatedUs, at.UnixMicro())
	}
	if models.vo2 != nil && fail["vo2"] == "" {
		out.ExternalVO2Max = am.ProjectVO2(*models.vo2, metadata["vo2"].UpdatedUs, at.UnixMicro())
	}
	if models.recovery != nil && fail["recovery"] == "" {
		out.PhysiologicalRecovery = am.ProjectRecovery(*models.recovery, metadata["recovery"].UpdatedUs, at.UnixMicro())
	}
	if models.readiness != nil && fail["readiness"] == "" {
		out.TrainingReadiness = am.ProjectReadiness(*models.readiness, metadata["readiness"].UpdatedUs, at.UnixMicro())
	}
	var daily *tl.DailyState
	if models.state != nil && fail["loadState"] == "" {
		for _, d := range models.state.Days {
			if d.Day == out.CalendarDate {
				v := d
				daily = &v
				break
			}
		}
		if daily == nil {
			fail["loadState"] = "technical_failure"
		}
	}
	if models.load != nil || daily != nil {
		r := metadata["loadState"]
		if daily == nil {
			r = metadata["loadWorkout"]
		}
		var workoutAt *int64
		if models.load != nil {
			v := metadata["loadWorkout"].EventEndUs
			workoutAt = &v
		}
		out.TrainingLoad = am.ProjectLoad(models.load, daily, workoutAt, r.StartedUs, r.UpdatedUs, at.UnixMicro())
	}
	failedSlots := []string{}
	for slot := range fail {
		failedSlots = append(failedSlots, slot)
	}
	sort.Slice(failedSlots, func(i, j int) bool {
		a, b := fail[failedSlots[i]], fail[failedSlots[j]]
		if a != b {
			return a != "technical_failure" && b == "technical_failure" || a < b && a != "technical_failure" && b != "technical_failure"
		}
		return failedSlots[i] < failedSlots[j]
	})
	for _, slot := range failedSlots {
		status := fail[slot]
		if status == "technical_failure" {
			out.ResponseState = "partial_response"
		}
		switch slot {
		case "loadState", "loadWorkout":
			out.TrainingLoad = am.Missing[am.TrainingLoadValue]("trainingLoad", at.UnixMicro(), status)
		case "ability":
			out.RunningAbility = am.Missing[am.RunningAbilityValue]("runningAbility", at.UnixMicro(), status)
		case "vo2":
			out.ExternalVO2Max = am.Missing[am.ExternalVO2Value]("externalVO2Max", at.UnixMicro(), status)
		case "recovery":
			out.PhysiologicalRecovery = am.Missing[am.RecoveryValue]("physiologicalRecovery", at.UnixMicro(), status)
		case "readiness":
			out.TrainingReadiness = am.Missing[am.ReadinessValue]("trainingReadiness", at.UnixMicro(), status)
		}
		var envelope *am.Envelope
		switch slot {
		case "loadState", "loadWorkout":
			envelope = &out.TrainingLoad.Envelope
		case "ability":
			envelope = &out.RunningAbility.Envelope
		case "vo2":
			envelope = &out.ExternalVO2Max.Envelope
		case "recovery":
			envelope = &out.PhysiologicalRecovery.Envelope
		case "readiness":
			envelope = &out.TrainingReadiness.Envelope
		}
		if envelope != nil {
			r := metadata[slot]
			if r.RunID != "" {
				envelope.ModelVersion = r.Algorithm + "/" + r.Version
			}
			if slot == "ability" && r.Algorithm == pf.CurveModel {
				envelope.ModelVersion = pf.AbilityModel + "/" + r.Version
			}
			envelope.PayloadVersion = r.ArtifactSchema
			if r.RunID != "" {
				envelope.UpdatedAt = &r.UpdatedUs
			}
		}
	}
	stats.MappingNS = time.Since(start).Nanoseconds()
	if e = tx.Commit(ctx); e != nil {
		return am.AthleteModelCandidateSnapshot{}, stats, e
	}
	return am.AthleteModelCandidateSnapshot{CandidateOnly: true, Snapshot: out}, stats, nil
}

func decodeProductArtifact(row dbgen.AthleteModelCurrentArtifactsRow, c modelReadContext, dataset string, m *candidateModels) ([]projectionRef, string) {
	refs := []projectionRef{}
	modelRef := func(scope, run, key, hash string) {
		refs = append(refs, projectionRef{Metric: row.Metric, Kind: "model", Scope: scope, Run: run, Key: key, Hash: hash})
	}
	factRef := func(id, kind, hash string) {
		refs = append(refs, projectionRef{Metric: row.Metric, Kind: "fact", ID: id, Type: kind, Hash: hash})
	}
	var identity struct {
		Dataset string `json:"datasetID"`
		Schema  string `json:"schemaVersion"`
	}
	if json.Unmarshal(row.Payload, &identity) != nil || identity.Dataset != dataset || identity.Schema != row.ArtifactSchema {
		return nil, "technical_failure"
	}
	if row.Version != "v1" {
		return nil, "unsupported"
	}
	// Existing channel assignment is sufficient; no mutable summary/assignment table.
	if c.assignment.StackID != wf.StackID() || c.configHash != wf.DefaultPolicy().Hash() {
		return nil, "unsupported"
	}
	switch row.Metric {
	case "loadWorkout":
		var r tl.Result
		if json.Unmarshal(row.Payload, &r) != nil || tl.Hash(r) != row.ContentHash || row.Algorithm != tl.Algorithm || r.Model != tl.Algorithm || r.ModelVersion != "v1" || r.Schema != tl.Schema || r.InputGeneration != row.Generation || r.ConfigHash != tl.ConfigHash() || r.Unit != tl.Unit {
			return nil, "technical_failure"
		}
		modelRef(wf.Scope(r.WorkoutID), r.FeatureArtifactID, "workout_features", "")
		m.load = &r
	case "loadState":
		var r tl.StateResult
		if json.Unmarshal(row.Payload, &r) != nil || tl.Hash(r) != row.ContentHash || row.Algorithm != tl.StateAlgorithm || r.Model != tl.StateAlgorithm || r.Version != "v1" || r.Schema != tl.StateSchema || r.Generation != row.Generation || r.ConfigHash != tl.ConfigHash() || r.Unit != tl.Unit+"/day" {
			return nil, "technical_failure"
		}
		var lin struct {
			Loads []tl.LedgerLoad `json:"loadLedger"`
		}
		if json.Unmarshal(row.Lineage, &lin) != nil {
			return nil, "technical_failure"
		}
		for _, f := range lin.Loads {
			modelRef(tl.Scope(f.WorkoutID), f.RunID, tl.ArtifactKey, "")
		}
		m.state = &r
	case "ability":
		var r pf.Result
		if json.Unmarshal(row.Payload, &r) != nil || pf.Hash(r) != row.ContentHash || row.Algorithm != pf.CurveModel || r.Model != pf.AbilityModel || r.Version != "v1" || r.Schema != pf.Schema || r.Generation != row.Generation || r.ConfigHash != pf.ConfigHash() {
			return nil, "technical_failure"
		}
		var hash string
		_ = json.Unmarshal(c.extensions["rai.performance_curve/v1"], &hash)
		if hash != r.ConfigHash {
			return nil, "sync_incomplete"
		}
		for _, e := range r.Evidence {
			modelRef(wf.Scope(e.WorkoutID), e.FeatureRunID, "workout_features", "")
		}
		m.ability = &r
	case "vo2":
		var r ve.Result
		if json.Unmarshal(row.Payload, &r) != nil || ve.Hash(r) != row.ContentHash || row.Algorithm != ve.Algorithm || r.ModelVersion != ve.Model || r.Schema != ve.Schema || r.Generation != row.Generation || r.ConfigHash != ve.DefaultConfig().Hash() {
			return nil, "technical_failure"
		}
		if !productExtensionCurrent(c, ve.Model, r.ConfigHash, r.Assignment, r.DatasetEpoch, r.Collector, "") {
			return nil, "sync_incomplete"
		}
		for _, f := range r.FactRefs {
			factRef(f[0], syncing.VO2Type, f[1])
		}
		m.vo2 = &r
	case "recovery":
		var r rc.Result
		if json.Unmarshal(row.Payload, &r) != nil || rc.Hash(r) != row.ContentHash || row.Algorithm != rc.Algorithm || r.ModelVersion != rc.Model || r.Schema != rc.Schema || r.Generation != row.Generation || r.ConfigHash != rc.DefaultConfig(c.timezone).Hash() {
			return nil, "technical_failure"
		}
		if !productExtensionCurrent(c, rc.Model, r.ConfigHash, r.Assignment, r.DatasetEpoch, r.Collector, r.Timezone) {
			return nil, "sync_incomplete"
		}
		for _, f := range r.FactRefs {
			factRef(f.ID, f.Type, f.Hash)
		}
		m.recovery = &r
	case "readiness":
		var r rd.Result
		if json.Unmarshal(row.Payload, &r) != nil || rd.Hash(r) != row.ContentHash || row.Algorithm != rd.Algorithm || r.ModelVersion != rd.Model || r.Schema != rd.Schema || r.Generation != row.Generation || r.ConfigHash != rd.DefaultConfig(c.timezone).Hash() {
			return nil, "technical_failure"
		}
		var lineage struct {
			Readiness struct {
				Assignment string `json:"assignmentID"`
			} `json:"readiness"`
		}
		if json.Unmarshal(row.Lineage, &lineage) != nil {
			return nil, "technical_failure"
		}
		if !productExtensionCurrent(c, rd.Model, r.ConfigHash, lineage.Readiness.Assignment, c.epoch, c.collector, r.Config.Timezone) {
			return nil, "sync_incomplete"
		}
		if r.Recovery != nil {
			modelRef(rc.Scope(c.timezone, r.Date), r.Recovery.RunID, "physiological_recovery", r.Recovery.Hash)
		}
		for _, f := range r.Refs {
			modelRef(tl.Scope(f.ID), f.RunID, tl.ArtifactKey, f.Hash)
		}
		m.readiness = &r
	default:
		return nil, "technical_failure"
	}
	return refs, ""
}
func productExtensionCurrent(c modelReadContext, model, hash, assignment, epoch, collector, tz string) bool {
	var entry struct {
		Hash       string `json:"configHash"`
		Assignment string `json:"assignmentID"`
		Timezone   string `json:"timezone"`
	}
	return json.Unmarshal(c.extensions[model], &entry) == nil && entry.Hash == hash && entry.Assignment == assignment && epoch == c.epoch && collector == c.collector && c.validSession && (tz == "" || tz == c.timezone && entry.Timezone == tz)
}
