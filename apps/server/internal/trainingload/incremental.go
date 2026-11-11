package trainingload

import (
	"sort"
	"time"
)

// StateCheckpoint lets the next day's v2 load state advance one day instead
// of replaying 186 days. It records the run after Day and a hash of the
// ledger it consumed; a later ledger that differs anywhere in that window
// (a new, changed or deleted Workout load) forces a full rebuild.
type StateCheckpoint struct {
	Day        string   `json:"day"`
	LedgerHash string   `json:"ledgerHash"`
	History    string   `json:"historyFrom"`
	Bounded    bool     `json:"bounded"`
	Run        stateRun `json:"run"`
}

// ledgerHash covers the ledger entries on or before through within the
// kernel seed window, independent of input order.
func ledgerHash(loads []LedgerLoad, from, through string) string {
	xs := []LedgerLoad{}
	for _, x := range loads {
		if x.Day >= from && x.Day <= through {
			x.RunID = ""
			xs = append(xs, x)
		}
	}
	sort.Slice(xs, func(i, j int) bool {
		if xs[i].Day != xs[j].Day {
			return xs[i].Day < xs[j].Day
		}
		return xs[i].WorkoutID < xs[j].WorkoutID
	})
	return Hash(xs)
}

// CalculateLoadStateV2Incremental computes one day (From == Through). When cp
// is the previous day's checkpoint over an identical ledger it advances one
// day; otherwise it rebuilds. It returns the result, the checkpoint for
// Through and whether the incremental path was taken. Both paths agree to
// floating-point accuracy (see TestIncrementalLoadStateMatchesRebuild).
func CalculateLoadStateV2Incremental(in StateInput, cp *StateCheckpoint) (StateResult, StateCheckpoint, bool, error) {
	if in.From != in.Through {
		return StateResult{}, StateCheckpoint{}, false, invalid()
	}
	day, err := time.Parse("2006-01-02", in.Through)
	if err != nil {
		return StateResult{}, StateCheckpoint{}, false, invalid()
	}
	prev := day.AddDate(0, 0, -1).Format("2006-01-02")
	// The day's ledger spans [D-186, D]; the checkpoint for D-1 hashed the
	// overlapping [D-186, D-1] part of its own ledger.
	window := day.AddDate(0, 0, -186).Format("2006-01-02")
	var seed *StateCheckpoint
	if cp != nil && cp.Bounded && cp.Day == prev && cp.History == in.HistoryFrom && cp.LedgerHash == ledgerHash(in.Loads, window, prev) {
		seed = cp
	}
	r, run, err := runLoadState(in, true, seed)
	if err != nil {
		return StateResult{}, StateCheckpoint{}, false, err
	}
	r.Schema, r.Model, r.Version, r.ConfigHash, r.Scope = StateSchemaV2, StateAlgorithmV2, VersionV2, ConfigHashV2(), ScopeStateV2()
	next := StateCheckpoint{Day: in.Through, LedgerHash: ledgerHash(in.Loads, day.AddDate(0, 0, -185).Format("2006-01-02"), in.Through), History: in.HistoryFrom, Bounded: true, Run: run}
	return r, next, seed != nil, nil
}
