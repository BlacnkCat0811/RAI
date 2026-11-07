package backfill

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Observation is the minimal immutable metric point compared by Diff. Value is
// nil for an explicit null; State/Reasons carry the supporting contract.
type Observation struct {
	Model   string   `json:"model"`
	Day     string   `json:"day"`
	State   string   `json:"state"`
	Value   *float64 `json:"value"`
	Unit    string   `json:"unit"`
	Version string   `json:"version"`
	Mode    string   `json:"mode"`
	Reasons []string `json:"reasons"`
}

// Snapshot is one view/as-of set of observations.
type Snapshot struct {
	View         string        `json:"view"`
	AsOf         string        `json:"asOf"`
	Observations []Observation `json:"observations"`
}

// DiffKind classifies one comparison entry.
type DiffKind string

const (
	DiffState       DiffKind = "state"
	DiffReason      DiffKind = "reason"
	DiffNull        DiffKind = "null"
	DiffValueBucket DiffKind = "value_bucket"
	DiffValue       DiffKind = "value"
	DiffBoundary    DiffKind = "boundary"
)

// Change is one explicit difference. From/To are labels, states, reason sets,
// bucket names or (only with DiffOptions.ExposeValues) formatted numbers.
type Change struct {
	Model  string   `json:"model"`
	Day    string   `json:"day"`
	Kind   DiffKind `json:"kind"`
	From   string   `json:"from"`
	To     string   `json:"to"`
	Reason string   `json:"reason"`
}

// DiffOptions controls the one non-default output: exact scalar values. The
// zero value is the product default and never emits a scalar health value.
type DiffOptions struct {
	ExposeValues bool `json:"exposeValues"`
}

// Diff compares two snapshots with the redacted default. It reports state
// changes, reason changes, null transitions, version/mode/unit boundaries and
// value bucket changes. Scalar values never appear in the default output.
func Diff(a, b Snapshot) []Change {
	return DiffWithOptions(a, b, DiffOptions{})
}

// DiffWithOptions is Diff with an explicit value-exposure decision. Exact
// values are intended for synthetic fixtures or approved private research
// output, never for the ordinary product diff.
func DiffWithOptions(a, b Snapshot, options DiffOptions) []Change {
	changes := make([]Change, 0)
	if a.View != b.View {
		changes = append(changes, Change{Model: "*", Day: "*", Kind: DiffBoundary, From: a.View, To: b.View, Reason: "view"})
	}
	left := indexSnapshot(a)
	right := indexSnapshot(b)

	keys := make([]observationKey, 0, len(left)+len(right))
	seen := make(map[observationKey]bool, len(left)+len(right))
	for key := range left {
		seen[key] = true
		keys = append(keys, key)
	}
	for key := range right {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].model != keys[j].model {
			return keys[i].model < keys[j].model
		}
		return keys[i].day < keys[j].day
	})

	for _, key := range keys {
		before, hasBefore := left[key]
		after, hasAfter := right[key]
		if !hasBefore {
			changes = append(changes, presenceChange(after, "observation_added"))
			continue
		}
		if !hasAfter {
			changes = append(changes, presenceChange(before, "observation_removed"))
			continue
		}
		changes = append(changes, compareObservation(before, after, options)...)
	}
	sortChanges(changes)
	return changes
}

type observationKey struct {
	model string
	day   string
}

func indexSnapshot(snapshot Snapshot) map[observationKey]Observation {
	index := make(map[observationKey]Observation, len(snapshot.Observations))
	for _, observation := range snapshot.Observations {
		index[observationKey{model: observation.Model, day: observation.Day}] = observation
	}
	return index
}

func presenceChange(observation Observation, reason string) Change {
	if observation.Value != nil {
		if reason == "observation_added" {
			return Change{Model: observation.Model, Day: observation.Day, Kind: DiffNull, From: "null", To: "value", Reason: reason}
		}
		return Change{Model: observation.Model, Day: observation.Day, Kind: DiffNull, From: "value", To: "null", Reason: reason}
	}
	if reason == "observation_added" {
		return Change{Model: observation.Model, Day: observation.Day, Kind: DiffState, From: "", To: observation.State, Reason: reason}
	}
	return Change{Model: observation.Model, Day: observation.Day, Kind: DiffState, From: observation.State, To: "", Reason: reason}
}

func compareObservation(a, b Observation, options DiffOptions) []Change {
	changes := make([]Change, 0, 5)
	if a.Version != b.Version {
		changes = append(changes, Change{Model: a.Model, Day: a.Day, Kind: DiffBoundary, From: a.Version, To: b.Version, Reason: "version"})
	}
	if a.Mode != b.Mode {
		changes = append(changes, Change{Model: a.Model, Day: a.Day, Kind: DiffBoundary, From: a.Mode, To: b.Mode, Reason: "mode"})
	}
	if a.Unit != b.Unit {
		changes = append(changes, Change{Model: a.Model, Day: a.Day, Kind: DiffBoundary, From: a.Unit, To: b.Unit, Reason: "unit"})
	}
	if a.State != b.State {
		changes = append(changes, Change{Model: a.Model, Day: a.Day, Kind: DiffState, From: a.State, To: b.State, Reason: "state"})
	}
	if before, after := canonicalReasons(a.Reasons), canonicalReasons(b.Reasons); strings.Join(before, ",") != strings.Join(after, ",") {
		changes = append(changes, Change{Model: a.Model, Day: a.Day, Kind: DiffReason, From: strings.Join(before, ","), To: strings.Join(after, ","), Reason: "reasons"})
	}
	switch {
	case a.Value == nil && b.Value == nil:
		// No value transition.
	case a.Value == nil:
		changes = append(changes, Change{Model: a.Model, Day: a.Day, Kind: DiffNull, From: "null", To: "value", Reason: "null_to_value"})
	case b.Value == nil:
		changes = append(changes, Change{Model: a.Model, Day: a.Day, Kind: DiffNull, From: "value", To: "null", Reason: "value_to_null"})
	case a.Version == b.Version && a.Mode == b.Mode && a.Unit == b.Unit && !sameValue(*a.Value, *b.Value):
		if options.ExposeValues {
			changes = append(changes, Change{
				Model: a.Model, Day: a.Day, Kind: DiffValue,
				From: formatValue(*a.Value), To: formatValue(*b.Value), Reason: "value",
			})
		} else {
			changes = append(changes, Change{
				Model: a.Model, Day: a.Day, Kind: DiffValueBucket,
				From: bucketValue(*a.Value), To: bucketValue(*b.Value), Reason: "value_bucket",
			})
		}
	}
	return changes
}

func sameValue(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.IsNaN(a) && math.IsNaN(b)
	}
	return a == b
}

const valueBucketWidth = 10.0

// bucketValue maps a scalar to an opaque bucket label. It intentionally never
// contains the input value, so the default diff cannot leak a health value.
func bucketValue(value float64) string {
	switch {
	case math.IsNaN(value):
		return "bin_nan"
	case math.IsInf(value, 0):
		return "bin_inf"
	case value > 1e9 || value < -1e9:
		return "bin_out_of_range"
	default:
		return "bin_" + strconv.Itoa(int(math.Floor(value/valueBucketWidth)))
	}
}

func formatValue(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func canonicalReasons(reasons []string) []string {
	out := append([]string(nil), reasons...)
	sort.Strings(out)
	deduped := out[:0]
	for _, reason := range out {
		if reason != "" && (len(deduped) == 0 || deduped[len(deduped)-1] != reason) {
			deduped = append(deduped, reason)
		}
	}
	return deduped
}

func sortChanges(changes []Change) {
	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].Day != changes[j].Day {
			return changes[i].Day < changes[j].Day
		}
		if changes[i].Model != changes[j].Model {
			return changes[i].Model < changes[j].Model
		}
		if changes[i].Kind != changes[j].Kind {
			return changes[i].Kind < changes[j].Kind
		}
		if changes[i].Reason != changes[j].Reason {
			return changes[i].Reason < changes[j].Reason
		}
		if changes[i].From != changes[j].From {
			return changes[i].From < changes[j].From
		}
		return changes[i].To < changes[j].To
	})
}

// String renders a change for logs without re-introducing hidden values; only
// the already-redacted fields are printed.
func (c Change) String() string {
	return fmt.Sprintf("%s/%s %s %s->%s (%s)", c.Model, c.Day, c.Kind, c.From, c.To, c.Reason)
}
