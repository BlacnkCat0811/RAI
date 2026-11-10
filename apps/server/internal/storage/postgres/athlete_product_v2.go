package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	am "rai/server/internal/athletemodel"
	"rai/server/internal/modelmanifest"
	pf "rai/server/internal/performance"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	"rai/server/internal/syncing"
	tl "rai/server/internal/trainingload"
	vo2 "rai/server/internal/vo2max"
	ve "rai/server/internal/vo2maxexternal"
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
}

func stampArtifact[T any](m *am.Metric[T], a publishedModelArtifact) {
	computed := a.PublishedAt.UnixMicro()
	if a.FinishedAt != nil {
		computed = a.FinishedAt.UnixMicro()
	}
	m.Envelope.MappingVersion = am.MappingVersion2
	validation := "unvalidated"
	if m.Envelope.Metric == "raiVO2Max" {
		validation = "experimental"
	}
	am.StampPublication(m, a.PublishedAt.UnixMicro(), computed, validation)
}

func (s *Store) ReadAthleteProduct(ctx context.Context, p syncing.Principal, at time.Time) (am.AthleteModelSnapshot, error) {
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
	out, err = readPublishedModelStack(ctx, tx, p.DatasetID, "active", c.assignment.StackID, out, c, at)
	if err != nil {
		return am.AthleteModelSnapshot{}, err
	}
	return out, tx.Commit(ctx)
}

// readPublishedModelStack projects the newest clean day-stack publication of
// every product slot. The caller owns the transaction and assignment channel.
func readPublishedModelStack(ctx context.Context, tx pgx.Tx, dataset, channel, stack string, out am.AthleteModelSnapshot, c modelReadContext, at time.Time) (am.AthleteModelSnapshot, error) {
	loc, err := time.LoadLocation(c.timezone)
	if err != nil {
		return am.AthleteModelSnapshot{}, ErrModelProjection
	}
	day := at.In(loc).Format("2006-01-02")
	scopes := map[string]string{
		"loadState": tl.ScopeStateV2For(stack),
		"ability":   pf.ScopeFor(stack),
		"vo2":       ve.ScopeFor(stack),
		"raiVO2":    vo2.ScopeFor(stack),
		"recovery":  rc.ScopeV2For(stack, day),
		"readiness": rd.ScopeV3For(stack),
	}
	// One bounded batch read for all six slots keeps the product request at a
	// fixed SELECT count and avoids N+1 artifact lookups.
	artifactKeys := map[string]string{"loadState": "load_state", "ability": pf.ArtifactKeyV2, "vo2": ve.ArtifactKey, "raiVO2": vo2.ArtifactKey, "recovery": "physiological_recovery", "readiness": "training_readiness"}
	byKey := make(map[string]string, len(scopes))
	keys := make([]string, 0, len(scopes))
	for name := range scopes {
		key := artifactKeys[name]
		keys = append(keys, key)
		byKey[key] = name
	}
	sort.Strings(keys)
	scopeNames := make([]string, 0, len(keys))
	for _, key := range keys {
		scope := scopes[byKey[key]]
		if stack == modelmanifest.StackID() {
			scope = modelmanifest.DayScope(stack, day)
		}
		scopeNames = append(scopeNames, scope)
	}
	rows := map[string]publishedModelArtifact{}
	query := `SELECT DISTINCT ON (a.artifact_key) a.artifact_key,a.payload,encode(a.content_sha256,'hex'),a.schema_version,a.artifact_key,r.algorithm_name,r.algorithm_version,r.id::text,r.target_generation,p.published_at,r.finished_at
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
		query = `SELECT DISTINCT ON (a.artifact_key) a.artifact_key,a.payload,encode(a.content_sha256,'hex'),a.schema_version,a.artifact_key,r.algorithm_name,r.algorithm_version,r.id::text,r.target_generation,p.published_at,r.finished_at
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
		if err = qrows.Scan(&key, &a.Payload, &a.ContentHash, &a.Schema, &a.Key, &a.Algorithm, &a.Version, &a.RunID, &a.Generation, &a.PublishedAt, &a.FinishedAt); err != nil {
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
	if a, ok := rows["loadState"]; ok {
		var r tl.StateResult
		if !validHistoryArtifact("trainingLoad", dataset, c.timezone, a) || json.Unmarshal(a.Payload, &r) != nil {
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
		if !validHistoryArtifact("runningAbility", dataset, c.timezone, a) || json.Unmarshal(a.Payload, &r) != nil {
			out.RunningAbility = am.Missing[am.RunningAbilityValue]("runningAbility", at.UnixMicro(), "technical_failure")
		} else {
			v := pf.Freshen(r, at.UnixMicro())
			out.RunningAbility = am.ProjectAbility(v, r.AsOf, a.PublishedAt.UnixMicro(), at.UnixMicro())
			stampArtifact(&out.RunningAbility, a)
		}
	}
	if a, ok := rows["vo2"]; ok {
		var r ve.Result
		if !validHistoryArtifact("externalVO2Max", dataset, c.timezone, a) || json.Unmarshal(a.Payload, &r) != nil {
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
		if !validHistoryArtifact("raiVO2Max", dataset, c.timezone, a) || json.Unmarshal(a.Payload, &r) != nil {
			m := am.Missing[am.RAIVO2Value]("raiVO2Max", at.UnixMicro(), "technical_failure")
			out.RAIVO2Max = &m
		} else {
			m := am.ProjectRAIVO2(r, a.PublishedAt.UnixMicro(), at.UnixMicro())
			stampArtifact(&m, a)
			out.RAIVO2Max = &m
		}
	}
	if a, ok := rows["recovery"]; ok {
		var r rc.V2Result
		if !validHistoryArtifact("physiologicalRecovery", dataset, c.timezone, a) || json.Unmarshal(a.Payload, &r) != nil {
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
	if a, ok := rows["readiness"]; ok {
		var r rd.Result
		if !validHistoryArtifact("trainingReadiness", dataset, c.timezone, a) || json.Unmarshal(a.Payload, &r) != nil {
			out.TrainingReadiness = am.Missing[am.ReadinessValue]("trainingReadiness", at.UnixMicro(), "technical_failure")
		} else {
			v, e := rd.FreshenV3(r, at.UnixMicro())
			if e != nil {
				out.TrainingReadiness = am.Missing[am.ReadinessValue]("trainingReadiness", at.UnixMicro(), "technical_failure")
			} else {
				out.TrainingReadiness = am.ProjectReadiness(v, a.PublishedAt.UnixMicro(), at.UnixMicro())
				stampArtifact(&out.TrainingReadiness, a)
			}
		}
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
	if stack == modelmanifest.StackID() {
		manifest, err := modelmanifest.CurrentHash()
		if err != nil || c.configHash != manifest {
			return page, ErrModelProjection
		}
		page.ManifestSHA = manifest
	}
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
	var scopes []string
	var key string
	switch metric {
	case "trainingLoad":
		scopes, key = []string{tl.ScopeStateV2For(stack)}, "load_state"
	case "runningAbility":
		scopes, key = []string{pf.ScopeFor(stack)}, pf.ArtifactKeyV2
	case "physiologicalRecovery":
		key = "physiological_recovery"
		for day := firstDay; !day.After(lastDay); day = day.AddDate(0, 0, 1) {
			scopes = append(scopes, rc.ScopeV2For(stack, day.Format(time.DateOnly)))
		}
	case "trainingReadiness":
		scopes, key = []string{rd.ScopeV3For(stack)}, "training_readiness"
	case "externalVO2Max":
		scopes, key = []string{ve.ScopeFor(stack)}, ve.ArtifactKey
	case "raiVO2Max":
		scopes, key = []string{vo2.ScopeFor(stack)}, vo2.ArtifactKey
	default:
		return page, ErrModelProjection
	}
	if stack == modelmanifest.StackID() {
		scopes = scopes[:0]
		for date := firstDay; !date.After(lastDay); date = date.AddDate(0, 0, 1) {
			scopes = append(scopes, modelmanifest.DayScope(stack, date.Format(time.DateOnly)))
		}
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (p.day) p.day::text,p.generation,encode(a.content_sha256,'hex'),a.payload,a.schema_version,r.algorithm_name,r.algorithm_version,p.published_at
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
		if err = rows.Scan(&day, &a.Generation, &a.ContentHash, &a.Payload, &a.Schema, &a.Algorithm, &a.Version, &a.PublishedAt); err != nil {
			return page, err
		}
		if len(page.Points) == limit {
			last := page.Points[limit-1]
			next := encodeHistoryCursor(historyCursor{DatasetID: p.DatasetID, Metric: metric, StackID: stack, AssignmentVersion: c.assignment.Version, View: view, From: from, To: to, Date: last.Date})
			page.NextCursor = &next
			break
		}
		if !validHistoryArtifact(metric, p.DatasetID, c.timezone, a) {
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

func validHistoryArtifact(metric, dataset, tz string, a publishedModelArtifact) bool {
	switch metric {
	case "trainingLoad":
		var r tl.StateResult
		return a.Schema == tl.StateSchemaV2 && a.Algorithm == tl.StateAlgorithmV2 && a.Version == tl.VersionV2 &&
			json.Unmarshal(a.Payload, &r) == nil && r.DatasetID == dataset && r.Generation == a.Generation &&
			r.Schema == a.Schema && r.ConfigHash == tl.ConfigHashV2() && tl.Hash(r) == a.ContentHash
	case "runningAbility":
		var r pf.Result
		return a.Schema == pf.SchemaV2 && a.Algorithm == pf.CurveModel && a.Version == pf.VersionV2 &&
			json.Unmarshal(a.Payload, &r) == nil && r.DatasetID == dataset && r.Generation == a.Generation &&
			r.Schema == a.Schema && r.ConfigHash == pf.ConfigHashV2() && pf.Hash(r) == a.ContentHash
	case "physiologicalRecovery":
		var r rc.V2Result
		return a.Schema == rc.SchemaV2 && a.Algorithm == rc.Algorithm && a.Version == "v2" &&
			json.Unmarshal(a.Payload, &r) == nil && r.Dataset == dataset && r.Generation == a.Generation &&
			r.Schema == a.Schema && r.ConfigHash == rc.DefaultV2Config(tz).Hash() && rc.Hash(r) == a.ContentHash
	case "trainingReadiness":
		var r rd.Result
		return a.Schema == rd.SchemaV3 && a.Algorithm == rd.Algorithm && a.Version == "v3" &&
			json.Unmarshal(a.Payload, &r) == nil && r.Dataset == dataset && r.Generation == a.Generation &&
			r.Schema == a.Schema && r.ConfigHash == rd.DefaultV3Config(tz).Hash() && rd.Hash(r) == a.ContentHash
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
			point.AsOf, status = v.AsOf, v.Status
		}
	case "trainingReadiness":
		var v rd.Result
		if json.Unmarshal(payload, &v) == nil {
			point.Value = v.Score
			point.Unit = "score_0_100"
			point.ConfigHash = v.ConfigHash
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
