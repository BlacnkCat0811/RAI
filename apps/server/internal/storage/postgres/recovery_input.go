package postgres

import (
	"context"
	"encoding/json"
	"time"
	_ "time/tzdata"

	"rai/server/internal/analysis"
	rc "rai/server/internal/recovery"
	"rai/server/internal/storage/postgres/dbgen"
	"rai/server/internal/syncing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type recoveryAssignment struct {
	Hash     string `json:"configHash"`
	ID       string `json:"assignmentID"`
	Timezone string `json:"timezone"`
	Prefix   string `json:"scopePrefix"`
}

func loadRecoveryFacts(ctx context.Context, tx pgx.Tx, l Lease, in *analysis.Snapshot, asOf, capture, day time.Time, epoch, collector, assignment string) error {
	return loadRecoveryFactsForConfig(ctx, tx, l, in, asOf, capture, day, epoch, collector, assignment, rc.DefaultConfig(day.Location().String()))
}

func loadRecoveryFactsForConfig(ctx context.Context, tx pgx.Tx, l Lease, in *analysis.Snapshot, asOf, capture, day time.Time, epoch, collector, assignment string, config rc.Config) error {
	loc := day.Location()
	through := day.AddDate(0, 0, 1)
	evaluation := asOf
	if !evaluation.Before(through) {
		evaluation = through.Add(-time.Microsecond)
	}
	input := &rc.Input{Dataset: l.DatasetID, DatasetEpoch: epoch, Collector: collector, Assignment: assignment, Generation: in.Generation, KnownThroughRevision: in.Revision, CapturedAt: capture.UnixMicro(), AsOf: evaluation.UnixMicro(), View: "revised", Config: config, Days: []rc.Day{}, Facts: []rc.Fact{}, Tombstones: []rc.Tombstone{}, Coverage: []rc.Coverage{}}
	for d := day.AddDate(0, 0, -input.Config.Window); !d.After(day); d = d.AddDate(0, 0, 1) {
		input.Days = append(input.Days, rc.Day{Date: d.Format("2006-01-02"), Start: d.UnixMicro(), End: d.AddDate(0, 0, 1).UnixMicro(), ClockBuckets: [4]int64{d.UnixMicro(), time.Date(d.Year(), d.Month(), d.Day(), 6, 0, 0, 0, loc).UnixMicro(), time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, loc).UnixMicro(), time.Date(d.Year(), d.Month(), d.Day(), 18, 0, 0, 0, loc).UnixMicro()}})
	}
	in.RecoveryInput = input
	in.InputVersion = rc.InputSchema
	in.From = day.AddDate(0, 0, -input.Config.Window-2).UTC()
	in.Through = evaluation.UTC()
	qs := dbgen.New(tx)
	rows, err := qs.RecoveryDailyFacts(ctx, dbgen.RecoveryDailyFactsParams{DatasetID: l.DatasetID, Column2: []string{rc.HRV, rc.RHR, rc.Sleep}, EventEnd: pgtype.Timestamptz{Time: in.From, Valid: true}, EventEnd_2: pgtype.Timestamptz{Time: evaluation.Add(time.Microsecond), Valid: true}})
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.SampleType != rc.Sleep && row.EventEnd.Time.UnixMicro() < input.Days[0].Start {
			continue
		}
		if len(input.Facts) >= rc.MaxFacts {
			return &analysis.Failure{Code: "analysis_input_limit"}
		}
		var meta map[string]json.RawMessage
		if json.Unmarshal(row.Metadata, &meta) != nil {
			return analysis.Invalid()
		}
		var source syncing.HealthKitSource
		if json.Unmarshal(meta["RAI:dailySource"], &source) != nil || source.BundleID == "" || source.BundleID != row.SourceBundleID {
			return analysis.Invalid()
		}
		f := rc.Fact{Type: row.SampleType, ID: row.SampleUuid, Hash: row.Hash, Start: row.EventStart.Time.UnixMicro(), End: row.EventEnd.Time.UnixMicro(), KnownAt: row.FirstReceivedAt.Time.UnixMicro(), Revision: row.EntityRevision}
		f.ContextIncomplete = source.Version == "" || source.Device == nil || source.Device.Model == "" || source.OperatingSystemVersion == ""
		if row.ValueNumeric.Valid {
			v := row.ValueNumeric.Float64
			f.Value = &v
		}
		if row.ValueCategory.Valid {
			v := int(row.ValueCategory.Int32)
			f.Category = &v
		}
		if row.SampleType == rc.HRV && row.Unit != "ms" || row.SampleType == rc.RHR && row.Unit != "count/min" {
			return analysis.Invalid()
		}
		if m, ok := meta["HKWasUserEntered"]; ok {
			if json.Unmarshal(m, &f.Manual) != nil {
				return analysis.Invalid()
			}
		}
		// Source revision/device and algorithm are real provenance. Quantity sample
		// duration separates SDNN acquisition contexts; no morning/watch inference.
		duration := int64(0)
		if f.Type == rc.HRV {
			duration = f.End - f.Start
		}
		f.Epoch = rc.Hash(struct {
			Source    syncing.HealthKitSource
			Algorithm json.RawMessage
			Duration  int64
		}{source, meta["HKAlgorithmVersion"], duration})
		// The stable identity drops app/OS/firmware versions and the SDNN
		// sample duration, so one Watch stays one source across updates.
		f.StableEpoch = rc.Hash(struct {
			Source    syncing.HealthKitSource
			Algorithm json.RawMessage
		}{source.StableIdentity(), meta["HKAlgorithmVersion"]})
		f.SourceRank = map[string]int{"apple_watch": 0, "associated_apple_watch": 0, "source_unknown": 1, "third_party": 2, "user_entered": 3}[source.Classification(nil)]
		if m, ok := meta["HKMetadataKeySyncIdentifier"]; ok {
			f.SyncKey = rc.Hash(struct {
				Bundle, Type string
				Key          json.RawMessage
			}{source.BundleID, f.Type, m})
		}
		f.SemanticHash = rc.Hash(struct {
			Type, Epoch string
			Start, End  int64
			Value       *float64
			Category    *int
			Metadata    map[string]json.RawMessage
		}{f.Type, f.Epoch, f.Start, f.End, f.Value, f.Category, meta})
		input.Facts = append(input.Facts, f)
	}
	tombs, err := qs.RecoveryDailyTombstones(ctx, dbgen.RecoveryDailyTombstonesParams{DatasetID: l.DatasetID, Column2: []string{rc.HRV, rc.RHR, rc.Sleep}, PreviousEnd: pgtype.Timestamptz{Time: in.From, Valid: true}, PreviousStart: pgtype.Timestamptz{Time: evaluation.Add(time.Microsecond), Valid: true}})
	if err != nil {
		return err
	}
	for _, row := range tombs {
		if row.SampleType != rc.Sleep && row.PreviousEnd.Time.UnixMicro() < input.Days[0].Start {
			continue
		}
		if len(input.Facts)+len(input.Tombstones) >= rc.MaxFacts {
			return &analysis.Failure{Code: "analysis_input_limit"}
		}
		x := rc.Tombstone{Type: row.SampleType, ID: row.TSampleUuid, Revision: row.DeletedRev, KnownAt: row.KnownAtUs}
		if row.PreviousStart.Valid {
			v := row.PreviousStart.Time.UnixMicro()
			x.Start = &v
		}
		if row.PreviousEnd.Valid {
			v := row.PreviousEnd.Time.UnixMicro()
			x.End = &v
		}
		x.Hash = rc.Hash(struct {
			Type, ID   string
			Revision   int64
			Start, End *int64
		}{x.Type, x.ID, x.Revision, x.Start, x.End})
		input.Tombstones = append(input.Tombstones, x)
	}
	cov, err := tx.Query(ctx, `SELECT s.sample_type,s.id::text,s.query_fingerprint,c.bootstrap_start_us,c.bootstrap_complete AND NOT c.has_more,c.last_discovery_at_us
 FROM daily_health_coverage c JOIN sync_streams s ON s.dataset_id=c.dataset_id AND s.id=c.stream_id WHERE c.dataset_id=$1 AND s.collector_epoch::text=$2 AND s.sample_type=ANY($3::text[]) ORDER BY s.sample_type,s.id LIMIT 7`, l.DatasetID, collector, []string{rc.HRV, rc.RHR, rc.Sleep})
	if err != nil {
		return err
	}
	defer cov.Close()
	counts := map[string]int{}
	for cov.Next() {
		var c rc.Coverage
		var id, fingerprint string
		var lower, known int64
		if err = cov.Scan(&c.Type, &id, &fingerprint, &lower, &c.Complete, &known); err != nil {
			return err
		}
		counts[c.Type]++
		if known > input.AsOf {
			known = input.AsOf
		}
		c.Through = known
		coverageStart := input.Days[0].Start
		if config.Policy == rc.PolicyV4 {
			// The extended horizon is a sparse-history fallback, not a new
			// requirement to bootstrap 180 days before current data can be used.
			coverageStart = day.AddDate(0, 0, -28).UnixMicro()
		}
		c.Complete = c.Complete && lower <= coverageStart
		c.Identity = rc.Hash(struct {
			Stream, Fingerprint string
			Lower               int64
		}{id, fingerprint, lower})
		input.Coverage = append(input.Coverage, c)
	}
	if err = cov.Err(); err != nil {
		return err
	}
	for i, c := range input.Coverage {
		if counts[c.Type] != 1 {
			input.Coverage[i].Complete = false
		}
	}
	*input = rc.Canonical(*input)
	return nil
}

// Past live rows cannot reconstruct withdrawals and old coverage. Original
// immutable run artifacts are the only supported historical audit surface.
