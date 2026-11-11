package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"rai/server/internal/analysis"
	"rai/server/internal/syncing"
	ve "rai/server/internal/vo2maxexternal"

	"github.com/jackc/pgx/v5"
)

func loadExternalFacts(ctx context.Context, tx pgx.Tx, l Lease, in *analysis.Snapshot, asOf, capture time.Time, epoch, collector, assignment string) error {
	a := &ve.ExternalVO2Snapshot{Dataset: l.DatasetID, Generation: in.Generation, AsOf: asOf.UnixMicro(), KnownThroughRevision: in.Revision, Config: ve.DefaultConfig(), View: "revised", Facts: []ve.Fact{}, Tombstones: []ve.Tombstone{}, Selections: []ve.Selection{}}
	in.ExternalInput = a
	in.InputVersion = ve.InputSchema
	in.From = asOf.Add(-90 * 24 * time.Hour)
	in.Through = asOf
	a.DatasetEpoch, a.Collector, a.Assignment, a.CapturedAt = epoch, collector, assignment, capture.UnixMicro()
	// Stream and observation are captured in this same repeatable-read transaction.
	// Multiple current streams are ambiguous rather than silently newest-wins.
	rows, err := tx.Query(ctx, `SELECT s.id::text,s.query_fingerprint,s.last_client_seq,c.bootstrap_start_us,c.query_completed_at_us,
 c.bootstrap_complete AND NOT c.has_more AND c.anchor_present,
 to_jsonb(c) FROM daily_health_coverage c JOIN sync_streams s ON s.dataset_id=c.dataset_id AND s.id=c.stream_id
 JOIN sync_batches b ON b.dataset_id=s.dataset_id AND b.batch_id=s.last_batch_id AND b.dataset_epoch=$3
 WHERE c.dataset_id=$1 AND s.sample_type='HKQuantityTypeIdentifierVO2Max' AND s.collector_epoch::text=$2 AND b.envelope->>'contract'='vo2max-facts/v1' LIMIT 2`, l.DatasetID, a.Collector, a.DatasetEpoch)
	if err != nil {
		return err
	}
	n := 0
	for rows.Next() {
		var c ve.Coverage
		var raw []byte
		if err = rows.Scan(&c.Stream, &c.Fingerprint, &c.Sequence, &c.BootstrapStart, &c.QueryCompletedAt, &c.Complete, &raw); err != nil {
			rows.Close()
			return err
		}
		c.Hash = hashJSON(raw)
		a.Coverage = c
		n++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if n != 1 {
		a.Coverage = ve.Coverage{}
	}
	rows, err = tx.Query(ctx, `SELECT id::text,encode(content_sha256,'hex'),effective_from,effective_through,created_at,COALESCE(supersedes_id::text,''),payload FROM athlete_model_inputs WHERE dataset_id=$1 AND kind='vo2max_source_selection' ORDER BY id LIMIT 129`, l.DatasetID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var x ve.Selection
		var from, recorded time.Time
		var through *time.Time
		var payload []byte
		if err = rows.Scan(&x.ID, &x.Hash, &from, &through, &recorded, &x.Supersedes, &payload); err != nil {
			rows.Close()
			return err
		}
		var p struct {
			Epoch   string `json:"sourceEpoch"`
			Revoked bool   `json:"revoked"`
		}
		if json.Unmarshal(payload, &p) != nil {
			rows.Close()
			return analysis.Invalid()
		}
		x.Epoch = p.Epoch
		x.Revoked = p.Revoked
		x.EffectiveFrom = from.UnixMicro()
		x.RecordedAt = recorded.UnixMicro()
		if through != nil {
			v := through.UnixMicro()
			x.EffectiveThrough = &v
		}
		a.Selections = append(a.Selections, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(a.Selections) > 128 {
		return &analysis.Failure{Code: "analysis_input_limit"}
	}
	sel, _ := ve.SelectionAt(*a)
	selectedEpoch := ""
	if sel != nil {
		selectedEpoch = sel.Epoch
	}
	lower := time.UnixMicro(a.AsOf - a.Config.ExpireUs)
	// Window uses a partial end-time index. The extra latest old tie is a bounded
	// indexed locator, never a lifetime-history rebuild or expired value fallback.
	const columns = `sample_uuid::text,event_start,event_end,value_numeric,COALESCE(unit,''),source_bundle_id,encode(content_sha256,'hex'),entity_revision,metadata`
	queries := []struct {
		sql  string
		args []any
	}{
		{`SELECT ` + columns + ` FROM health_samples WHERE dataset_id=$1 AND sample_type='HKQuantityTypeIdentifierVO2Max' AND event_end>$2 ORDER BY event_end DESC,sample_uuid LIMIT 1025`, []any{l.DatasetID, lower}},
	}
	if selectedEpoch != "" {
		queries = append(queries, struct {
			sql  string
			args []any
		}{`SELECT ` + columns + ` FROM health_samples WHERE dataset_id=$1 AND sample_type='HKQuantityTypeIdentifierVO2Max' AND metadata->'RAI:vo2maxFactV1'->>'sourceEpochID'=$2 AND event_end=(SELECT event_end FROM health_samples WHERE dataset_id=$1 AND sample_type='HKQuantityTypeIdentifierVO2Max' AND metadata->'RAI:vo2maxFactV1'->>'sourceEpochID'=$2 AND event_end<=$3 ORDER BY event_end DESC LIMIT 1) LIMIT 1025`, []any{l.DatasetID, selectedEpoch, lower}})
	}
	seenFacts := map[string]bool{}
	baseQueries := len(queries)
	for qi := 0; qi < len(queries); qi++ {
		q := queries[qi]
		rows, err = tx.Query(ctx, q.sql, q.args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var f ve.Fact
			var start, end time.Time
			var value *float64
			var unit, bundle string
			var raw []byte
			if err = rows.Scan(&f.ID, &start, &end, &value, &unit, &bundle, &f.Hash, &f.KnownRevision, &raw); err != nil {
				rows.Close()
				return err
			}
			if seenFacts[f.ID] {
				continue
			}
			seenFacts[f.ID] = true
			f.Start = start.UnixMicro()
			f.End = end.UnixMicro()
			if value != nil {
				f.Value = *value
			}
			var meta map[string]json.RawMessage
			if json.Unmarshal(raw, &meta) != nil {
				rows.Close()
				return analysis.Invalid()
			}
			var c struct {
				Schema     string                         `json:"schemaVersion"`
				Provenance syncing.VO2Provenance          `json:"provenance"`
				Epoch      string                         `json:"sourceEpochID"`
				Metadata   map[string]syncing.VO2Metadata `json:"typedMetadata"`
			}
			if body, ok := meta[syncing.VO2Reserved]; ok {
				if json.Unmarshal(body, &c) != nil || c.Schema != syncing.VO2FactSchema || c.Epoch != c.Provenance.EpochID() || bundle != c.Provenance.SourceRevision.Source.BundleIdentifier || value == nil || unit != ve.Unit {
					rows.Close()
					return analysis.Invalid()
				}
				f.Epoch, f.StableEpoch = c.Epoch, c.Provenance.StableEpochID()
				fact := syncing.VO2Fact{UUID: f.ID, StartUs: f.Start, EndUs: f.End, Quantity: syncing.Quantity{Value: f.Value, Unit: unit}, Provenance: c.Provenance, Metadata: c.Metadata}
				f.Origin, f.OriginReason = fact.Origin()
				f.Supported = true
				f.SupportHash = syncing.DailyDigest(syncing.VO2Canonical(map[string]any{"provenance": c.Provenance, "metadata": c.Metadata}))
				semantic := fact
				semantic.UUID = ""
				f.SemanticHash = syncing.DailyDigest(syncing.VO2Canonical(semantic))
				if m, ok := c.Metadata["HKVO2MaxTestType"]; ok {
					v := m.Integer()
					f.TestType = &v
				}
				if m, ok := c.Metadata["HKMetadataKeyAppleDeviceCalibrated"]; ok {
					v := m.Boolean()
					f.Calibrated = &v
				}
				if m, ok := c.Metadata["HKMetadataKeySyncIdentifier"]; ok {
					var syncID string
					if json.Unmarshal(m.Value, &syncID) != nil {
						rows.Close()
						return analysis.Invalid()
					}
					f.SyncKey = ve.Hash([]string{bundle, syncing.VO2Type, syncID})
				}
			} else {
				f.Origin = "unclassified"
				f.OriginReason = "unsupported_metadata"
			}
			a.Facts = append(a.Facts, f)
			if len(a.Facts) > ve.MaxRefs {
				rows.Close()
				return &analysis.Failure{Code: "analysis_input_limit"}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if qi == baseQueries-1 && sel != nil && hasExternalSync(a.Facts) {
			queries = append(queries, struct {
				sql  string
				args []any
			}{`SELECT y.sample_uuid::text,y.event_start,y.event_end,y.value_numeric,COALESCE(y.unit,''),y.source_bundle_id,encode(y.content_sha256,'hex'),y.entity_revision,y.metadata FROM (
 SELECT DISTINCT source_bundle_id,metadata->'RAI:vo2maxFactV1'->'typedMetadata'->'HKMetadataKeySyncIdentifier' AS sid FROM health_samples
 WHERE dataset_id=$1 AND sample_type='HKQuantityTypeIdentifierVO2Max' AND sample_uuid=ANY($2::uuid[]) AND metadata->'RAI:vo2maxFactV1'->>'sourceEpochID'=$5
 AND metadata->'RAI:vo2maxFactV1'->'typedMetadata'->'HKMetadataKeySyncIdentifier' IS NOT NULL) k
 CROSS JOIN LATERAL (SELECT * FROM health_samples y WHERE y.dataset_id=$1 AND y.sample_type='HKQuantityTypeIdentifierVO2Max'
 AND y.source_bundle_id=k.source_bundle_id AND y.metadata->'RAI:vo2maxFactV1'->'typedMetadata'->'HKMetadataKeySyncIdentifier'=k.sid
 AND y.event_end<=$3 AND y.event_end<=$4 LIMIT 1025) y LIMIT 1025`, []any{l.DatasetID, factIDs(a.Facts), lower, asOf, selectedEpoch}})
		}
	}
	rows, err = tx.Query(ctx, `SELECT sample_uuid::text,deleted_rev,previous_end,provenance_snapshot FROM entity_tombstones WHERE dataset_id=$1 AND sample_type='HKQuantityTypeIdentifierVO2Max' AND previous_end>$2 ORDER BY previous_end DESC LIMIT 1025`, l.DatasetID, lower)
	if err != nil {
		return err
	}
	if err = loadExternalTombstones(rows, a); err != nil {
		return err
	}
	if selectedEpoch != "" {
		rows, err = tx.Query(ctx, `SELECT sample_uuid::text,deleted_rev,previous_end,provenance_snapshot FROM entity_tombstones WHERE dataset_id=$1 AND sample_type='HKQuantityTypeIdentifierVO2Max' AND provenance_snapshot->>'sourceEpochID'=$2 AND previous_end=(SELECT previous_end FROM entity_tombstones WHERE dataset_id=$1 AND sample_type='HKQuantityTypeIdentifierVO2Max' AND provenance_snapshot->>'sourceEpochID'=$2 AND previous_end<=$3 ORDER BY previous_end DESC LIMIT 1) LIMIT 1025`, l.DatasetID, selectedEpoch, lower)
		if err != nil {
			return err
		}
		if err = loadExternalTombstones(rows, a); err != nil {
			return err
		}
	}
	// Unknown deletions are withdrawal evidence only, never an invented source.
	rows, err = tx.Query(ctx, `SELECT sample_uuid::text,deleted_rev,previous_end,provenance_snapshot FROM entity_tombstones WHERE dataset_id=$1 AND sample_type='HKQuantityTypeIdentifierVO2Max' AND previous_end IS NULL ORDER BY deleted_rev DESC LIMIT 1`, l.DatasetID)
	if err != nil {
		return err
	}
	if err = loadExternalTombstones(rows, a); err != nil {
		return err
	}
	*a = ve.Canonical(*a)
	return nil
}
func factIDs(fs []ve.Fact) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.ID)
	}
	return out
}
func hashJSON(raw []byte) string {
	var v any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&v) != nil {
		return ""
	}
	return ve.Hash(v)
}
func loadExternalTombstones(rows pgx.Rows, a *ve.ExternalVO2Snapshot) error {
	defer rows.Close()
	for rows.Next() {
		var t ve.Tombstone
		var end *time.Time
		var raw []byte
		if err := rows.Scan(&t.ID, &t.Revision, &end, &raw); err != nil {
			return err
		}
		if end != nil {
			v := end.UnixMicro()
			t.End = &v
		}
		var c struct {
			Provenance syncing.VO2Provenance          `json:"provenance"`
			Epoch      string                         `json:"sourceEpochID"`
			Metadata   map[string]syncing.VO2Metadata `json:"originInputs"`
		}
		if raw != nil {
			if json.Unmarshal(raw, &c) != nil || c.Epoch != c.Provenance.EpochID() {
				return analysis.Invalid()
			}
			t.Epoch, t.StableEpoch = c.Epoch, c.Provenance.StableEpochID()
			t.Origin, _ = (syncing.VO2Fact{Provenance: c.Provenance, Metadata: c.Metadata}).Origin()
		}
		t.Hash = ve.Hash(struct {
			ID       string
			Revision int64
			Context  string
		}{t.ID, t.Revision, hashJSON(raw)})
		a.Tombstones = append(a.Tombstones, t)
		if len(a.Facts)+len(a.Tombstones) > ve.MaxRefs {
			return &analysis.Failure{Code: "analysis_input_limit"}
		}
	}
	return rows.Err()
}

// Explicitly unavailable: current live raw cannot reconstruct deleted history.

func hasExternalSync(fs []ve.Fact) bool {
	for _, f := range fs {
		if f.SyncKey != "" {
			return true
		}
	}
	return false
}

// stableExternalInput rewrites a loaded external VO2 input to stable source
// identities (v2 stack only): facts and tombstones use their StableEpoch, and
// a selection naming any version of a source selects the whole source.
func stableExternalInput(in *ve.ExternalVO2Snapshot) {
	stable := map[string]string{}
	for _, f := range in.Facts {
		if f.StableEpoch != "" {
			stable[f.Epoch] = f.StableEpoch
		}
	}
	for _, t := range in.Tombstones {
		if t.StableEpoch != "" {
			stable[t.Epoch] = t.StableEpoch
		}
	}
	for i := range in.Facts {
		if in.Facts[i].StableEpoch != "" {
			in.Facts[i].Epoch = in.Facts[i].StableEpoch
		}
	}
	for i := range in.Tombstones {
		if in.Tombstones[i].StableEpoch != "" {
			in.Tombstones[i].Epoch = in.Tombstones[i].StableEpoch
		}
	}
	for i := range in.Selections {
		if s, ok := stable[in.Selections[i].Epoch]; ok {
			in.Selections[i].Epoch = s
		}
	}
}

// VO2Sources lists the VO2 sources of the last year, one per stable source,
// each named by its latest full epoch (what a selection stores).
func (s *Store) VO2Sources(ctx context.Context, p syncing.Principal) ([]ve.Source, error) {
	if !syncing.ValidID(p.DatasetID) {
		return nil, syncing.Fail(409, "dataset_unavailable")
	}
	rows, err := s.Pool.Query(ctx, `SELECT event_end,metadata->'RAI:vo2maxFactV1' FROM health_samples
 WHERE dataset_id=$1 AND sample_type='HKQuantityTypeIdentifierVO2Max' AND event_end>now()-interval '365 days'
 AND metadata ? 'RAI:vo2maxFactV1' ORDER BY event_end DESC LIMIT 2000`, p.DatasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byStable := map[string]*ve.Source{}
	order := []string{}
	for rows.Next() {
		var end time.Time
		var raw []byte
		if err := rows.Scan(&end, &raw); err != nil {
			return nil, err
		}
		var c struct {
			Provenance syncing.VO2Provenance `json:"provenance"`
			Epoch      string                `json:"sourceEpochID"`
		}
		if json.Unmarshal(raw, &c) != nil || c.Epoch == "" {
			continue
		}
		key := c.Provenance.StableEpochID()
		if x, ok := byStable[key]; ok {
			x.Samples++
			continue
		}
		x := &ve.Source{SourceEpoch: c.Epoch, Bundle: c.Provenance.SourceRevision.Source.BundleIdentifier, LatestDate: end.UTC().Format(time.DateOnly), Samples: 1}
		if c.Provenance.SourceRevision.ProductType != nil {
			x.ProductType = *c.Provenance.SourceRevision.ProductType
		}
		byStable[key] = x
		order = append(order, key)
	}
	out := []ve.Source{}
	for _, k := range order {
		out = append(out, *byStable[k])
	}
	return out, rows.Err()
}
