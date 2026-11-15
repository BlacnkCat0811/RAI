package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"rai/server/internal/analysis"
	"rai/server/internal/hrref"
	rc "rai/server/internal/recovery"
	"rai/server/internal/storage/postgres/dbgen"
	"rai/server/internal/syncing"
)

func restingHRSuggestion(ctx context.Context, tx pgx.Tx, dataset string, now time.Time, loc *time.Location) (*hrref.RestingSuggestion, error) {
	rows, err := dbgen.New(tx).RecoveryDailyFacts(ctx, dbgen.RecoveryDailyFactsParams{DatasetID: dataset, Column2: []string{rc.RHR},
		EventEnd: pgtype.Timestamptz{Time: now.AddDate(0, 0, -29), Valid: true}, EventEnd_2: pgtype.Timestamptz{Time: now, Valid: true}})
	if err != nil {
		return nil, err
	}
	if len(rows) > 8192 {
		return nil, &analysis.Failure{Code: "analysis_input_limit"}
	}
	bySource := map[string]*rc.Input{}
	for _, row := range rows {
		if !row.ValueNumeric.Valid || row.Unit != "count/min" || row.FirstReceivedAt.Time.After(now) {
			continue
		}
		var meta map[string]json.RawMessage
		var source syncing.HealthKitSource
		var manual bool
		if json.Unmarshal(row.Metadata, &meta) != nil || json.Unmarshal(meta["RAI:dailySource"], &source) != nil || source.BundleID == "" || source.BundleID != row.SourceBundleID || source.Classification(nil) == "user_entered" {
			continue
		}
		if raw, ok := meta["HKWasUserEntered"]; ok && (json.Unmarshal(raw, &manual) != nil || manual) {
			continue
		}
		epoch := hrref.Hash(struct {
			Source    syncing.HealthKitSource
			Algorithm json.RawMessage
		}{source.StableIdentity(), meta["HKAlgorithmVersion"]})
		if bySource[epoch] == nil {
			bySource[epoch] = &rc.Input{}
		}
		v := row.ValueNumeric.Float64
		bySource[epoch].Facts = append(bySource[epoch].Facts, rc.Fact{Type: rc.RHR, ID: row.SampleUuid, Hash: row.Hash,
			Start: row.EventStart.Time.UnixMicro(), End: row.EventEnd.Time.UnixMicro(), KnownAt: row.FirstReceivedAt.Time.UnixMicro(), Value: &v})
	}
	var best *hrref.RestingSuggestion
	bestEpoch := ""
	for epoch, facts := range bySource {
		candidate := hrref.SuggestResting(dailyRHRFacts(facts, now.UnixMicro(), epoch, loc), now.UnixMicro())
		if candidate == nil {
			continue
		}
		if best == nil || candidate.LatestSampleAt.After(best.LatestSampleAt) || candidate.LatestSampleAt.Equal(best.LatestSampleAt) && (candidate.SampleDays > best.SampleDays || candidate.SampleDays == best.SampleDays && epoch < bestEpoch) {
			best, bestEpoch = candidate, epoch
		}
	}
	return best, nil
}
