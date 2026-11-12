package postgres

import (
	"encoding/json"
	"sort"
	"time"

	rc "rai/server/internal/recovery"
)

// Effective/revoked input semantics are shared with Workout references. For
// recovery only the explicitly entered RHR is needed; HRmax is not an input.
func recoveryRestingHR(inputs []stackInput, at, captured time.Time) (*rc.TypedRHR, bool) {
	rows := effectiveStackInputs(inputs, at, captured)
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].From.Equal(rows[j].From) {
			return rows[i].From.After(rows[j].From)
		}
		return rows[i].ID < rows[j].ID
	})
	var best *rc.TypedRHR
	var effective time.Time
	for _, row := range rows {
		if row.Kind != "hr_reference" {
			continue
		}
		var x struct {
			Rest float64   `json:"restingHR"`
			At   time.Time `json:"restingMeasuredAt"`
		}
		if json.Unmarshal(row.Payload, &x) != nil || x.Rest < 30 || x.Rest >= 220 || x.At.IsZero() || x.At.After(at) || x.At.After(row.Known) {
			continue
		}
		if best != nil {
			if row.From.Equal(effective) && x.Rest != best.Value {
				return best, true
			}
			if row.From.Before(effective) {
				break
			}
			continue
		}
		best = &rc.TypedRHR{Value: x.Rest, ID: row.ID, Hash: row.Hash, MeasuredAt: x.At.UnixMicro(), KnownAt: row.Known.UnixMicro()}
		effective = row.From
	}
	return best, false
}
