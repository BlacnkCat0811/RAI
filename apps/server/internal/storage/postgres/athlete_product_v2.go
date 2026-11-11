package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"
	"time"

	am "rai/server/internal/athletemodel"
	"rai/server/internal/modelmanifest"
	"rai/server/internal/modelrelease"
	pf "rai/server/internal/performance"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	ra "rai/server/internal/runningability"
	sr "rai/server/internal/sessionrecovery"
	"rai/server/internal/syncing"
	tl "rai/server/internal/trainingload"
	vo2 "rai/server/internal/vo2max"
	ve "rai/server/internal/vo2maxexternal"

	"github.com/jackc/pgx/v5"
)

type publishedModelArtifact struct {
	Payload     []byte
	ContentHash string
	Schema      string
	Key         string
	Algorithm   string
	Version     string
	RunID       string
	Generation  int64
	PublishedAt time.Time
	FinishedAt  *time.Time
	ManifestSHA string
}

func stampArtifact[T any](m *am.Metric[T], a publishedModelArtifact) {
	computed := a.PublishedAt.UnixMicro()
	if a.FinishedAt != nil {
		computed = a.FinishedAt.UnixMicro()
	}
	m.Envelope.MappingVersion = am.MappingVersion2
	validation := "unvalidated"
	if m.Envelope.Mode != "" {
		m.Envelope.MappingVersion = am.MappingVersion3
	}
	if m.Envelope.Metric == "raiVO2Max" {
		validation = "experimental"
	}
	am.StampPublication(m, a.PublishedAt.UnixMicro(), computed, validation)
}

func (s *Store) ReadAthleteProduct(ctx context.Context, p syncing.Principal, at time.Time) (am.AthleteModelSnapshot, error) {
	return s.readAthleteProduct(ctx, p, at, false)
}
func (s *Store) ReadAthleteProductV3(ctx context.Context, p syncing.Principal, at time.Time) (am.AthleteModelSnapshot, error) {
	return s.readAthleteProduct(ctx, p, at, true)
}
func (s *Store) readAthleteProduct(ctx context.Context, p syncing.Principal, at time.Time, schema3 bool) (am.AthleteModelSnapshot, error) {
	if !syncing.ValidID(p.DatasetID) || at.IsZero() {
		return am.AthleteModelSnapshot{}, ErrModelProjection
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return am.AthleteModelSnapshot{}, err
	}
	defer tx.Rollback(ctx)
	out, c, err := readModelContext(ctx, tx, p.DatasetID, "active", at)
	if err != nil {
		return out, err
	}
	if c.assignment.StackID == "" || c.assignment.StackID == "legacy" {
		return out, am.ErrNotActivated
	}
	out = am.NewSnapshotV2(at.UnixMicro(), c.revision, c.timezone, out.CalendarDate, c.assignment)
	if schema3 {
		out = am.NewSnapshotV3(at.UnixMicro(), c.revision, c.timezone, out.CalendarDate, c.assignment)
	}
	out, err = readPublishedModelStack(ctx, tx, p.DatasetID, "active", c.assignment.StackID, out, c, at)
	if err != nil {
		return am.AthleteModelSnapshot{}, err
	}
	return out, tx.Commit(ctx)
}

// readPublishedModelStack projects the newest clean day-stack publication of
// every product slot. The caller owns the transaction and assignment channel.
func readPublishedModelStack(ctx context.Context, tx pgx.Tx, dataset, channel, stack string, out am.AthleteModelSnapshot, c modelReadContext, at time.Time) (am.AthleteModelSnapshot, error) {
	release, supported := modelrelease.Lookup(stack, c.configHash, c.timezone)
	if !supported {
		am.SetMissingMetricsV2(&out, "unsupported")
		am.SetProjectedResponseState(&out)
		return out, nil
	}
	if release.Manifest.ProductSchema == am.Schema3 && out.SchemaVersion != am.Schema3 {
		am.SetMissingMetricsV2(&out, "model_update_required")
		am.SetProjectedResponseState(&out)
		return out, nil
	}
	if release.Manifest.ProductSchema != am.Schema3 && out.SchemaVersion == am.Schema3 {
		out = am.NewSnapshotV2(at.UnixMicro(), c.revision, c.timezone, out.CalendarDate, c.assignment)
	}
	var assignedManifest string
	if json.Unmarshal(c.extensions[modelmanifest.Schema], &assignedManifest) != nil || assignedManifest != c.configHash {
		return out, ErrModelProjection
	}
	loc, err := time.LoadLocation(c.timezone)
	if err != nil {
		return am.AthleteModelSnapshot{}, ErrModelProjection
	}
	day := at.In(loc).Format("2006-01-02")
	if stack != modelmanifest.StackID() {
		// Only the current release can be computed by this worker. Retained
		// clean artifacts remain readable; absent ones require an upgrade.
		am.SetMissingMetricsV2(&out, "model_update_required")
	}
	// One bounded batch read for all six slots keeps the product request at a
	// fixed SELECT count and avoids N+1 artifact lookups.
	artifactKeys := map[string]string{"loadState": "load_state", "ability": pf.ArtifactKeyV2, "vo2": ve.ArtifactKey, "raiVO2": vo2.ArtifactKey, "recovery": "physiological_recovery", "readiness": "training_readiness"}
	if out.SchemaVersion == am.Schema3 && release.Manifest.ProductSchema == am.Schema3 {
		artifactKeys["session"] = sr.Key
		artifactKeys["estimate"] = ra.Key
		artifactKeys["focus"] = "load_focus"
	}
	byKey := make(map[string]string, len(artifactKeys))
	keys := make([]string, 0, len(artifactKeys))
	for name, key := range artifactKeys {
		keys = append(keys, key)
		byKey[key] = name
	}
	sort.Strings(keys)
	scopeNames := make([]string, 0, len(keys))
	for range keys {
		scopeNames = append(scopeNames, modelmanifest.DayScope(stack, day))
	}
	rows := map[string]publishedModelArtifact{}
	query := `SELECT DISTINCT ON (a.artifact_key) a.artifact_key,a.payload,encode(a.content_sha256,'hex'),a.schema_version,a.artifact_key,r.algorithm_name,r.algorithm_version,r.id::text,r.target_generation,p.published_at,r.finished_at,r.lineage->>'manifestSHA256'
FROM unnest($2::text[], $4::text[]) expected(scope, key)
JOIN analysis_publications p ON p.scope_key=expected.scope
JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.scope_key=p.scope_key AND r.target_generation=p.generation
JOIN analysis_published_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id AND a.artifact_key=expected.key
JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key AND d.generation=p.generation AND d.clean_generation=p.generation
WHERE p.dataset_id=$1 AND r.status='SUCCEEDED' AND p.day<=$3::date
ORDER BY a.artifact_key,p.day DESC,p.published_run_id`
	args := []any{dataset, scopeNames, day, keys}
	if channel != "active" {
		// The product view deliberately hides candidate/previous channels. An
		// administrative preview reads the same clean publications from the base
		// tables with an explicit channel binding.
		query = `SELECT DISTINCT ON (a.artifact_key) a.artifact_key,a.payload,encode(a.content_sha256,'hex'),a.schema_version,a.artifact_key,r.algorithm_name,r.algorithm_version,r.id::text,r.target_generation,p.published_at,r.finished_at,r.lineage->>'manifestSHA256'
FROM unnest($2::text[], $4::text[]) expected(scope, key)
JOIN analysis_publications p ON p.scope_key=expected.scope AND p.dataset_id=$1
JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.scope_key=p.scope_key AND r.target_generation=p.generation
JOIN analysis_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id AND a.artifact_key=expected.key
JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key AND d.generation=p.generation AND d.clean_generation=p.generation
JOIN analysis_model_channels m ON m.dataset_id=$1 AND m.channel=$5 AND m.stack_id=$6
WHERE r.status='SUCCEEDED' AND p.day<=$3::date
ORDER BY a.artifact_key,p.day DESC,p.published_run_id`
		args = append(args, channel, stack)
	}
	qrows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return am.AthleteModelSnapshot{}, err
	}
	for qrows.Next() {
		var key string
		var a publishedModelArtifact
		if err = qrows.Scan(&key, &a.Payload, &a.ContentHash, &a.Schema, &a.Key, &a.Algorithm, &a.Version, &a.RunID, &a.Generation, &a.PublishedAt, &a.FinishedAt, &a.ManifestSHA); err != nil {
			qrows.Close()
			return am.AthleteModelSnapshot{}, err
		}
		if name, ok := byKey[key]; ok {
			rows[name] = a
		}
	}
	if err = qrows.Err(); err != nil {
		qrows.Close()
		return am.AthleteModelSnapshot{}, err
	}
	qrows.Close()
	if len(rows) == 0 && stack == modelmanifest.StackID() {
		var failed bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs j JOIN dirty_scopes d USING(dataset_id,scope_key)
 WHERE j.dataset_id=$1 AND j.scope_key=$2 AND j.target_generation=d.generation
 AND j.kind='analysis.model_stack_v2' AND j.state='failed')`, dataset, modelmanifest.DayScope(stack, day)).Scan(&failed); err != nil {
			return out, err
		}
		if failed {
			am.SetMissingMetricsV2(&out, "technical_failure")
		}
	}
	if a, ok := rows["loadState"]; ok {
		var r tl.StateResult
		if !validHistoryArtifactForRelease("trainingLoad", dataset, c.timezone, a, release) || json.Unmarshal(a.Payload, &r) != nil {
			out.TrainingLoad = am.Missing[am.TrainingLoadValue]("trainingLoad", at.UnixMicro(), "technical_failure")
		} else {
			var daily *tl.DailyState
			for i := range r.Days {
				if r.Days[i].Day == day {
					daily = &r.Days[i]
					break
				}
			}
			if daily == nil {
				out.TrainingLoad = am.Missing[am.TrainingLoadValue]("trainingLoad", at.UnixMicro(), "sync_incomplete")
			} else {
				out.TrainingLoad = am.ProjectLoadV2(daily, a.PublishedAt.UnixMicro(), a.PublishedAt.UnixMicro(), at.UnixMicro())
				stampArtifact(&out.TrainingLoad, a)
			}
		}
	}
	if a, ok := rows["ability"]; ok {
		var r pf.Result
		if !validHistoryArtifactForRelease("runningAbility", dataset, c.timezone, a, release) || json.Unmarshal(a.Payload, &r) != nil {
			out.RunningAbility = am.Missing[am.RunningAbilityValue]("runningAbility", at.UnixMicro(), "technical_failure")
		} else {
			v := pf.Freshen(r, at.UnixMicro())
			out.RunningAbility = am.ProjectAbility(v, r.AsOf, a.PublishedAt.UnixMicro(), at.UnixMicro())
			stampArtifact(&out.RunningAbility, a)
		}
	}
	if a, ok := rows["vo2"]; ok {
		var r ve.Result
		if !validHistoryArtifactForRelease("externalVO2Max", dataset, c.timezone, a, release) || json.Unmarshal(a.Payload, &r) != nil {
			out.ExternalVO2Max = am.Missing[am.ExternalVO2Value]("externalVO2Max", at.UnixMicro(), "technical_failure")
		} else {
			v, e := ve.Freshen(r, at.UnixMicro())
			if e != nil {
				out.ExternalVO2Max = am.Missing[am.ExternalVO2Value]("externalVO2Max", at.UnixMicro(), "technical_failure")
			} else {
				out.ExternalVO2Max = am.ProjectVO2(v, a.PublishedAt.UnixMicro(), at.UnixMicro())
				stampArtifact(&out.ExternalVO2Max, a)
			}
		}
	}
	if a, ok := rows["raiVO2"]; ok {
		var r vo2.Result
		if !validHistoryArtifactForRelease("raiVO2Max", dataset, c.timezone, a, release) || json.Unmarshal(a.Payload, &r) != nil {
			m := am.Missing[am.RAIVO2Value]("raiVO2Max", at.UnixMicro(), "technical_failure")
			out.RAIVO2Max = &m
		} else {
			m := am.ProjectRAIVO2(r, a.PublishedAt.UnixMicro(), at.UnixMicro())
			stampArtifact(&m, a)
			out.RAIVO2Max = &m
		}
	}
	if a, ok := rows["recovery"]; ok && a.Schema != rc.SchemaV3 {
		var r rc.V2Result
		if !validHistoryArtifactForRelease("physiologicalRecovery", dataset, c.timezone, a, release) || json.Unmarshal(a.Payload, &r) != nil {
			out.PhysiologicalRecovery = am.Missing[am.RecoveryValue]("physiologicalRecovery", at.UnixMicro(), "technical_failure")
		} else {
			v, e := rc.Freshen(r.Result, at.UnixMicro())
			if e != nil {
				out.PhysiologicalRecovery = am.Missing[am.RecoveryValue]("physiologicalRecovery", at.UnixMicro(), "technical_failure")
			} else {
				out.PhysiologicalRecovery = am.ProjectRecovery(v, a.PublishedAt.UnixMicro(), at.UnixMicro())
				stampArtifact(&out.PhysiologicalRecovery, a)
			}
		}
	}
	if a, ok := rows["readiness"]; ok && a.Schema != rd.SchemaV4 {
		var r rd.Result
		if !validHistoryArtifactForRelease("trainingReadiness", dataset, c.timezone, a, release) || json.Unmarshal(a.Payload, &r) != nil {
			out.TrainingReadiness = am.Missing[am.ReadinessValue]("trainingReadiness", at.UnixMicro(), "technical_failure")
		} else {
			v, e := rd.FreshenV3ForConfig(r, at.UnixMicro(), release.ReadinessConfig)
			if e != nil {
				out.TrainingReadiness = am.Missing[am.ReadinessValue]("trainingReadiness", at.UnixMicro(), "technical_failure")
			} else {
				out.TrainingReadiness = am.ProjectReadiness(v, a.PublishedAt.UnixMicro(), at.UnixMicro())
				stampArtifact(&out.TrainingReadiness, a)
			}
		}
	}
	if release.Manifest.ProductSchema == am.Schema3 {
		projectOptimizedArtifacts(&out, rows, dataset, c.timezone, release, at.UnixMicro())
	}
	am.SetProjectedResponseState(&out)
	return out, nil
}

func (s *Store) ReadAthleteHistory(ctx context.Context, p syncing.Principal, metric, from, to string, limit int, cursor, view string) (am.HistoryPage, error) {
	page := am.HistoryPage{Metric: metric, View: view, Points: []am.HistoryPoint{}}
	if limit < 1 || limit > 90 {
		return page, am.ErrHistoryCursor
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return page, err
	}
	defer tx.Rollback(ctx)
	snapshot, c, err := readModelContext(ctx, tx, p.DatasetID, "active", time.Now())
	if err != nil {
		return page, err
	}
	stack := c.assignment.StackID
	if stack == "" || stack == "legacy" {
		return page, am.ErrNotActivated
	}
	if view == "as_known" {
		// Current publication pointers cannot reconstruct what was known on a
		// past day. Withdrawn or superseded facts are deliberately not used.
		return page, am.ErrHistoricalInputUnavailable
	}
	page.Assignment = c.assignment
	release, supported := modelrelease.Lookup(stack, c.configHash, c.timezone)
	if !supported {
		return page, am.ErrUnsupportedRelease
	}
	var assignedManifest string
	if json.Unmarshal(c.extensions[modelmanifest.Schema], &assignedManifest) != nil || assignedManifest != c.configHash {
		return page, ErrModelProjection
	}
	if metric == "sessionRecovery" && release.Manifest.SessionRecovery == nil || metric == "abilityEstimate" && release.Manifest.AbilityEstimate == nil || metric == "loadFocus" && release.Manifest.LoadFocus == nil {
		return page, am.ErrUnsupportedRelease
	}
	page.ManifestSHA = c.configHash
	if to == "" {
		to = snapshot.CalendarDate
	}
	lastDay, err := time.Parse(time.DateOnly, to)
	if err != nil {
		return page, am.ErrHistoryCursor
	}
	if from == "" {
		from = lastDay.AddDate(0, 0, -28).Format(time.DateOnly)
	}
	firstDay, err := time.Parse(time.DateOnly, from)
	if err != nil || lastDay.Before(firstDay) || lastDay.Sub(firstDay) > 365*24*time.Hour {
		return page, am.ErrHistoryCursor
	}
	var cursorDate any
	if cursor != "" {
		cur, err := decodeHistoryCursor(cursor)
		if err != nil || cur.DatasetID != p.DatasetID || cur.Metric != metric || cur.StackID != stack || cur.AssignmentVersion != c.assignment.Version || cur.View != view || cur.From != from || cur.To != to || cur.Date < from || cur.Date > to {
			return page, am.ErrHistoryCursor
		}
		if _, err = time.Parse(time.DateOnly, cur.Date); err != nil {
			return page, am.ErrHistoryCursor
		}
		cursorDate = cur.Date
	}
	var key string
	switch metric {
	case "trainingLoad":
		key = "load_state"
	case "runningAbility":
		key = pf.ArtifactKeyV2
	case "physiologicalRecovery":
		key = "physiological_recovery"
	case "trainingReadiness":
		key = "training_readiness"
	case "externalVO2Max":
		key = ve.ArtifactKey
	case "raiVO2Max":
		key = vo2.ArtifactKey
	case "sessionRecovery":
		key = sr.Key
	case "loadFocus":
		key = "load_focus"
	case "abilityEstimate":
		key = ra.Key
	default:
		return page, ErrModelProjection
	}
	var scopes []string
	for date := firstDay; !date.After(lastDay); date = date.AddDate(0, 0, 1) {
		scopes = append(scopes, modelmanifest.DayScope(stack, date.Format(time.DateOnly)))
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (p.day) p.day::text,p.generation,encode(a.content_sha256,'hex'),a.payload,a.schema_version,r.algorithm_name,r.algorithm_version,p.published_at,r.lineage->>'manifestSHA256'
FROM analysis_publications p
JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.scope_key=p.scope_key AND r.target_generation=p.generation
JOIN analysis_published_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id AND a.artifact_key=$7
JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key AND d.generation=p.generation AND d.clean_generation=p.generation
WHERE p.dataset_id=$1 AND p.scope_key=ANY($2) AND p.day BETWEEN $3::date AND $4::date
AND ($6::date IS NULL OR p.day < $6::date)
ORDER BY p.day DESC,p.published_at DESC,p.published_run_id LIMIT $5`, p.DatasetID, scopes, from, to, limit+1, cursorDate, key)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var day string
		var a publishedModelArtifact
		if err = rows.Scan(&day, &a.Generation, &a.ContentHash, &a.Payload, &a.Schema, &a.Algorithm, &a.Version, &a.PublishedAt, &a.ManifestSHA); err != nil {
			return page, err
		}
		if len(page.Points) == limit {
			last := page.Points[limit-1]
			next := encodeHistoryCursor(historyCursor{DatasetID: p.DatasetID, Metric: metric, StackID: stack, AssignmentVersion: c.assignment.Version, View: view, From: from, To: to, Date: last.Date})
			page.NextCursor = &next
			break
		}
		if !validHistoryArtifactForRelease(metric, p.DatasetID, c.timezone, a, release) {
			return page, ErrModelProjection
		}
		page.Points = append(page.Points, historyPoint(metric, day, a.Algorithm, a.Version, a.Schema, a.PublishedAt, a.Payload))
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return page, err
	}
	page.Trend = historyTrend(page.Points)
	return page, tx.Commit(ctx)
}

func validHistoryArtifactForStack(metric, dataset, tz, stack string, a publishedModelArtifact) bool {
	release, ok := modelrelease.Lookup(stack, a.ManifestSHA, tz)
	return ok && validHistoryArtifactForRelease(metric, dataset, tz, a, release)
}

func validHistoryArtifactForRelease(metric, dataset, tz string, a publishedModelArtifact, release modelrelease.Release) bool {
	manifest, err := modelmanifest.Hash(release.Manifest)
	if err != nil || a.ManifestSHA != manifest {
		return false
	}
	switch metric {
	case "trainingLoad":
		var r tl.StateResult
		return a.Schema == tl.StateSchemaV2 && a.Algorithm == tl.StateAlgorithmV2 && a.Version == tl.VersionV2 &&
			json.Unmarshal(a.Payload, &r) == nil && r.DatasetID == dataset && r.Generation == a.Generation &&
			r.Schema == a.Schema && r.ConfigHash == release.LoadConfigHash && tl.Hash(r) == a.ContentHash
	case "runningAbility":
		var r pf.Result
		return a.Schema == pf.SchemaV2 && a.Algorithm == pf.CurveModel && a.Version == pf.VersionV2 &&
			json.Unmarshal(a.Payload, &r) == nil && r.DatasetID == dataset && r.Generation == a.Generation &&
			r.Schema == a.Schema && r.ConfigHash == release.AbilityConfigHash && pf.Hash(r) == a.ContentHash
	case "physiologicalRecovery":
		if release.Manifest.Recovery.Version == "v3" {
			var r rc.V3Result
			return a.Schema == rc.SchemaV3 && a.Algorithm == rc.Algorithm && a.Version == "v3" && json.Unmarshal(a.Payload, &r) == nil && r.Dataset == dataset && r.Generation == a.Generation && r.Timezone == tz && r.Schema == a.Schema && r.ConfigHash == release.RecoveryConfig.Hash() && rc.Hash(r) == a.ContentHash
		}
		var r rc.V2Result
		return a.Schema == rc.SchemaV2 && a.Algorithm == rc.Algorithm && a.Version == "v2" &&
			json.Unmarshal(a.Payload, &r) == nil && r.Dataset == dataset && r.Generation == a.Generation &&
			r.Schema == a.Schema && r.Timezone == tz && r.ConfigHash == release.RecoveryConfig.Hash() && rc.Hash(r) == a.ContentHash
	case "trainingReadiness":
		if release.Manifest.Readiness.Version == "v4" {
			var r rd.V4Result
			return a.Schema == rd.SchemaV4 && a.Algorithm == rd.Algorithm && a.Version == "v4" && json.Unmarshal(a.Payload, &r) == nil && r.Dataset == dataset && r.Generation == a.Generation && r.Schema == a.Schema && r.Config == release.ReadinessConfig && r.ConfigHash == release.ReadinessConfig.Hash() && rd.Hash(r) == a.ContentHash
		}
		var r rd.Result
		return a.Schema == rd.SchemaV3 && a.Algorithm == rd.Algorithm && a.Version == "v3" &&
			json.Unmarshal(a.Payload, &r) == nil && r.Dataset == dataset && r.Generation == a.Generation &&
			r.Schema == a.Schema && r.Config.Timezone == tz && r.Config == release.ReadinessConfig && r.ConfigHash == release.ReadinessConfig.Hash() && rd.Hash(r) == a.ContentHash
	case "externalVO2Max":
		var r ve.Result
		return a.Schema == ve.Schema && a.Algorithm == ve.Algorithm && a.Version == "v1" &&
			json.Unmarshal(a.Payload, &r) == nil && r.Dataset == dataset && r.Generation == a.Generation &&
			r.Schema == a.Schema && r.ModelVersion == ve.Model && ve.Hash(r) == a.ContentHash
	case "raiVO2Max":
		var r vo2.Result
		return a.Schema == vo2.Schema && a.Algorithm == vo2.Model && a.Version == vo2.Version &&
			json.Unmarshal(a.Payload, &r) == nil && r.DatasetID == dataset && r.Generation == a.Generation &&
			r.Schema == a.Schema && r.ModelVersion == vo2.Version && r.Experimental && vo2.Hash(r) == a.ContentHash
	case "sessionRecovery":
		var r sr.Result
		return release.Manifest.SessionRecovery != nil && a.Schema == sr.Schema && a.Algorithm == sr.Algorithm && a.Version == "v1" && json.Unmarshal(a.Payload, &r) == nil && r.Dataset == dataset && r.Generation == a.Generation && r.Schema == a.Schema && r.ConfigHash == release.Manifest.SessionRecovery.ConfigHash && sr.Hash(r) == a.ContentHash
	case "abilityEstimate":
		var r ra.Result
		return release.Manifest.AbilityEstimate != nil && a.Schema == ra.Schema && a.Algorithm == ra.Algorithm && a.Version == "v1" && json.Unmarshal(a.Payload, &r) == nil && r.Dataset == dataset && r.Generation == a.Generation && r.Schema == a.Schema && r.ConfigHash == release.Manifest.AbilityEstimate.ConfigHash && ra.Hash(r) == a.ContentHash
	case "loadFocus":
		var r tl.FocusResult
		return release.Manifest.LoadFocus != nil && a.Schema == tl.FocusSchema && a.Algorithm == tl.FocusAlgorithm && a.Version == "v1" && json.Unmarshal(a.Payload, &r) == nil && r.Dataset == dataset && r.Generation == a.Generation && r.Schema == a.Schema && r.ConfigHash == release.Manifest.LoadFocus.ConfigHash && tl.Hash(r) == a.ContentHash
	default:
		return false
	}
}

// historyPoint decodes the published typed artifact into the one scalar that
// the history endpoint exposes. It deliberately does not re-run any model or
// read raw samples. A missing scalar remains nil with an explicit unavailable
// state; it is never replaced with zero.
func historyPoint(metric, day, algorithm, version, schema string, publishedAt time.Time, payload []byte) am.HistoryPoint {
	point := am.HistoryPoint{Date: day, Metric: metric, ModelVersion: algorithm + "/" + version, PayloadVersion: schema, State: "unavailable", ReasonCodes: []string{}, AsOf: publishedAt.UnixMicro()}
	var status string
	switch metric {
	case "trainingLoad":
		var v tl.StateResult
		if json.Unmarshal(payload, &v) == nil {
			for _, d := range v.Days {
				if d.Day == day {
					point.Value = d.Acute.Value
					point.Unit = v.Unit + "/day"
					for key, x := range map[string]*float64{"chronic": d.Chronic.Value, "balance": d.Balance, "dailyLoad": d.RecordedLoad} {
						if x != nil {
							if point.Components == nil {
								point.Components = map[string]float64{}
							}
							point.Components[key] = *x
						}
					}
					point.ConfigHash = v.ConfigHash
					status = d.Quality
					break
				}
			}
		}
	case "runningAbility":
		var v pf.Result
		if json.Unmarshal(payload, &v) == nil {
			if v.Ability != nil {
				score := v.Ability.Score
				point.Value = &score
			}
			point.Unit = "score_0_100"
			point.ConfigHash, point.Mode = v.ConfigHash, v.EvidenceMode
			point.AsOf, status = v.AsOf, v.Status
		}
	case "physiologicalRecovery":
		var v rc.Result
		if json.Unmarshal(payload, &v) == nil {
			point.Value = v.Score
			point.Unit = "score_0_100"
			point.ConfigHash = v.ConfigHash
			var mode struct {
				Mode string `json:"mode"`
			}
			_ = json.Unmarshal(payload, &mode)
			point.Mode = mode.Mode
			point.AsOf, status = v.AsOf, v.Status
		}
	case "trainingReadiness":
		var v rd.Result
		if json.Unmarshal(payload, &v) == nil {
			point.Value = v.Score
			point.Unit = "score_0_100"
			point.ConfigHash = v.ConfigHash
			var mode struct {
				Mode string `json:"mode"`
			}
			_ = json.Unmarshal(payload, &mode)
			point.Mode = mode.Mode
			point.AsOf, status = v.AsOf, v.Status
		}
	case "raiVO2Max":
		var v vo2.Result
		if json.Unmarshal(payload, &v) == nil {
			point.Value, point.Unit = v.Value, v.Unit
			point.Source = v.SourceEpoch
			if v.CandidateOnly {
				point.Value = nil
			}
			if v.Experimental {
				point.Mode = "experimental"
			}
			point.AsOf, status = v.AsOf, v.Status
		}
	case "sessionRecovery":
		var v sr.Result
		if json.Unmarshal(payload, &v) == nil {
			point.Value = v.RemainingHours
			point.Unit = "hours"
			point.AsOf = v.AsOf
			status = v.Status
			point.ConfigHash = v.ConfigHash
			point.Mode = v.Mode
			if v.Range != nil {
				point.Components = map[string]float64{"low": v.Range.Low, "high": v.Range.High}
			}
		}
	case "loadFocus":
		var v tl.FocusResult
		if json.Unmarshal(payload, &v) == nil {
			point.ConfigHash = v.ConfigHash
			point.Mode = "observed"
			point.Unit = "RAI_AU"
			point.AsOf = v.AsOf
			status = "available"
			point.Components = map[string]float64{}
			for _, w := range v.Windows {
				if w.WindowDays == 7 {
					total := w.Buckets.Total()
					point.Value = &total
					point.Components = map[string]float64{"lowAerobicAU": w.Buckets.Low, "highAerobicAU": w.Buckets.High, "anaerobicProxyAU": w.Buckets.Anaerobic, "unclassifiedAU": w.Buckets.Unclassified, "unknownWorkouts": float64(w.UnknownWorkouts)}
				}
			}
		}
	case "abilityEstimate":
		var v ra.Result
		if json.Unmarshal(payload, &v) == nil {
			b := v.Demonstrated
			switch v.SelectedMode {
			case "maximal_supported":
				b = v.Maximal
			case "submax_estimated":
				b = v.Submax
			case "source_conflict":
				b.Score = nil
			}
			point.Value = b.Score
			point.Unit = "score_0_100"
			point.Mode = v.SelectedMode
			point.Source = b.Source
			point.EvidenceAt = &b.EvidenceAt
			point.AsOf = v.AsOf
			point.ConfigHash = v.ConfigHash
			status = b.Status
			point.ReasonCodes = v.Reasons
		}
	case "externalVO2Max":
		var v ve.Result
		if json.Unmarshal(payload, &v) == nil {
			point.Value, point.Unit = v.Value, v.Unit
			point.ConfigHash, point.Mode = v.ConfigHash, v.Method
			if v.SourceEpoch != nil {
				point.Source = *v.SourceEpoch
			}
			point.AsOf, status = v.AsOf, v.Status
		}
	}
	if point.Value != nil {
		if status == "complete" {
			status = "available"
		}
		point.State = status
		if point.State == "" {
			point.State = "available"
		}
	}
	return point
}

// historyTrend picks the real point closest to a 28-day lookback from the
// newest returned point. A missing value, a model/payload version change or a
// range shorter than 28 days yields no delta; no interpolation is performed.
func historyTrend(points []am.HistoryPoint) *am.HistoryTrend {
	if len(points) < 2 {
		return nil
	}
	newest := points[0]
	newestDate, err := time.Parse("2006-01-02", newest.Date)
	if err != nil || newest.Value == nil {
		return nil
	}
	best := -1
	bestDistance := 1 << 30
	for i := 1; i < len(points); i++ {
		d, err := time.Parse("2006-01-02", points[i].Date)
		if err != nil {
			continue
		}
		days := int(newestDate.Sub(d).Hours() / 24)
		if days < 28 || points[i].Value == nil {
			continue
		}
		distance := days - 28
		if distance < 0 {
			distance = -distance
		}
		if distance < bestDistance || (distance == bestDistance && (best == -1 || points[i].Date < points[best].Date)) {
			best, bestDistance = i, distance
		}
	}
	if best == -1 {
		return nil
	}
	old := points[best]
	for _, p := range points[1 : best+1] {
		if p.ModelVersion == newest.ModelVersion && p.PayloadVersion == newest.PayloadVersion && p.ConfigHash == newest.ConfigHash && p.Mode == newest.Mode && p.Source == newest.Source && p.Unit == newest.Unit {
			continue
		}
		return &am.HistoryTrend{Kind: "descriptive_delta", FromDate: old.Date, ToDate: newest.Date, Days: int(newestDate.Sub(mustDate(old.Date)).Hours() / 24), Unit: newest.Unit, ReasonCodes: []string{"history_trend_context_break"}}
	}
	delta := *newest.Value - *old.Value
	return &am.HistoryTrend{Kind: "descriptive_delta", FromDate: old.Date, ToDate: newest.Date, Days: int(newestDate.Sub(mustDate(old.Date)).Hours() / 24), FromValue: old.Value, ToValue: newest.Value, Delta: &delta, Unit: newest.Unit, ReasonCodes: []string{}}
}

func mustDate(s string) time.Time { d, _ := time.Parse("2006-01-02", s); return d }
func encodeHistoryCursor(c historyCursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeHistoryCursor(s string) (historyCursor, error) {
	var c historyCursor
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(raw) > 1024 {
		return c, am.ErrHistoryCursor
	}
	if err = json.Unmarshal(raw, &c); err != nil {
		return c, am.ErrHistoryCursor
	}
	return c, nil
}

type historyCursor struct {
	DatasetID         string `json:"d"`
	Metric            string `json:"m"`
	StackID           string `json:"s"`
	AssignmentVersion int64  `json:"a"`
	View              string `json:"v"`
	From              string `json:"f"`
	To                string `json:"t"`
	Date              string `json:"p"`
}
