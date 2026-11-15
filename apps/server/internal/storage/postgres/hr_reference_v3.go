package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"rai/server/internal/hrref"
	"rai/server/internal/modelmanifest"
	"rai/server/internal/syncing"
	"time"
)

func referenceEvents(inputs []stackInput, target, asOf time.Time) []hrref.Event {
	var out []hrref.Event
	for _, row := range effectiveStackInputs(inputs, target, asOf) {
		if row.Kind != "hr_reference" {
			continue
		}
		e := hrref.Event{ID: row.ID, Hash: row.Hash, Provenance: row.Provenance, From: row.From, Known: row.Known, Through: row.Through, Order: row.Order}
		if row.Schema == hrref.InputSchemaV3 {
			if json.Unmarshal(row.Payload, &e.Patch) != nil {
				continue
			}
		} else {
			var x struct {
				Max    float64    `json:"maxHR"`
				Rest   *float64   `json:"restingHR"`
				At     *time.Time `json:"measuredAt"`
				RestAt *time.Time `json:"restingMeasuredAt"`
				Method string     `json:"method"`
			}
			if json.Unmarshal(row.Payload, &x) != nil || x.Max <= 0 {
				continue
			}
			e.Legacy = true
			e.Patch.MaxHR = &hrref.Setting{Value: x.Max, Basis: "user_setting", Method: x.Method, MeasuredAt: x.At, Unit: "bpm"}
			e.Patch.RestingHR = &hrref.RestSetting{Mode: "automatic"}
			if x.Rest != nil {
				e.Patch.RestingHR.Mode = "manual"
				e.Patch.RestingHR.ManualValue = &hrref.Setting{Value: *x.Rest, Basis: "user_setting", Method: x.Method, MeasuredAt: x.RestAt, Unit: "bpm"}
			}
		}
		out = append(out, e)
	}
	return out
}
func (s *Store) HRSettings(ctx context.Context, p syncing.Principal) (hrref.SettingsResponse, error) {
	var out hrref.SettingsResponse
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	now := time.Now()
	inputs, err := stackInputs(ctx, tx, p.DatasetID, now)
	if err != nil {
		return out, err
	}
	events := referenceEvents(inputs, now, now)
	out.Saved = hrref.Saved(events, now, now)
	var stack, tz string
	err = tx.QueryRow(ctx, `SELECT COALESCE(c.stack_id,''),d.timezone FROM datasets d LEFT JOIN analysis_model_channels c ON c.dataset_id=d.id AND c.channel='active' WHERE d.id=$1`, p.DatasetID).Scan(&stack, &tz)
	if err != nil {
		return out, err
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return out, err
	}
	out.ObservedResting, err = restingHRSuggestion(ctx, tx, p.DatasetID, now, loc)
	if err != nil {
		return out, err
	}
	// Suggestions are independent of the active calculation stack. Old stacks
	// still have no v3 calculation evidence; do not claim they use it.
	if stack != modelmanifest.StackID() {
		return out, tx.Commit(ctx)
	}
	facts, err := stackRestingHRFacts(ctx, tx, p.DatasetID, now.AddDate(0, 0, -29), now, now)
	if err != nil {
		return out, err
	}
	_, out.Resolved, err = hrref.ResolveSettings(events, hrref.Input{AsOf: now.UnixMicro(), WorkoutStart: now.UnixMicro(), SourceEpoch: "settings", DailyRHR: dailyRHRFacts(facts, now.UnixMicro(), "settings", loc)}, stack)
	if err != nil && !errors.Is(err, hrref.ErrConflict) {
		return out, err
	}
	return out, tx.Commit(ctx)
}
