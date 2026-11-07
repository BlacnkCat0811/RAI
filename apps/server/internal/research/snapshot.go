package research

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrInvalidSnapshot marks structurally unusable prospective snapshots.
	ErrInvalidSnapshot = errors.New("research: invalid prospective snapshot")
	// ErrFutureLeak marks a known-at violation that would leak future data.
	ErrFutureLeak = errors.New("research: future data leak")
)

// CreateSnapshot validates and returns a defensive copy of s.
//
// It rejects an empty dataset or model identity, a missing AsOf, a KnownAt
// after AsOf (future leakage), and duplicate athlete+prediction-time rows.
// Duplicate rows are rejected regardless of cluster: one athlete may not have
// two predictions stamped at the same instant in the same snapshot.
func CreateSnapshot(s ProspectiveSnapshot) (ProspectiveSnapshot, error) {
	if strings.TrimSpace(s.DatasetID) == "" {
		return ProspectiveSnapshot{}, fmt.Errorf("%w: dataset id is required", ErrInvalidSnapshot)
	}
	if strings.TrimSpace(s.Model) == "" {
		return ProspectiveSnapshot{}, fmt.Errorf("%w: model is required", ErrInvalidSnapshot)
	}
	if s.AsOf.IsZero() {
		return ProspectiveSnapshot{}, fmt.Errorf("%w: asOf is required", ErrInvalidSnapshot)
	}
	if s.KnownAt.After(s.AsOf) {
		return ProspectiveSnapshot{}, fmt.Errorf("%w: knownAt %s is after asOf %s",
			ErrFutureLeak, s.KnownAt.UTC().Format(time.RFC3339Nano), s.AsOf.UTC().Format(time.RFC3339Nano))
	}

	seen := make(map[string]int, len(s.Observations))
	for i, observation := range s.Observations {
		key := observation.AthleteKey + "\x00" + observation.PredictedAt.UTC().Format(time.RFC3339Nano)
		if previous, ok := seen[key]; ok {
			return ProspectiveSnapshot{}, fmt.Errorf(
				"%w: duplicate athlete/time at observations %d and %d (%q, %s)",
				ErrInvalidSnapshot, previous, i, observation.AthleteKey,
				observation.PredictedAt.UTC().Format(time.RFC3339Nano))
		}
		seen[key] = i
	}

	return cloneSnapshot(s), nil
}

// FreezeKnownAt returns a snapshot containing only observations whose model
// features were known at or before cutoff. Metadata and withdrawal history are
// preserved; the input snapshot is not modified.
func FreezeKnownAt(s ProspectiveSnapshot, cutoff time.Time) ProspectiveSnapshot {
	out := s
	out.Observations = make([]Prediction, 0, len(s.Observations))
	for _, observation := range s.Observations {
		if !observation.FeaturesKnownAt.After(cutoff) {
			out.Observations = append(out.Observations, clonePrediction(observation))
		}
	}
	return out
}

// VerifyNoFutureLeak checks that every prediction in s was built from features
// known no later than the snapshot AsOf. It also rejects a snapshot whose own
// KnownAt is after AsOf, even when the snapshot bypassed CreateSnapshot.
func VerifyNoFutureLeak(s ProspectiveSnapshot) error {
	if s.KnownAt.After(s.AsOf) {
		return fmt.Errorf("%w: snapshot knownAt %s is after asOf %s",
			ErrFutureLeak, s.KnownAt.UTC().Format(time.RFC3339Nano), s.AsOf.UTC().Format(time.RFC3339Nano))
	}
	for i, observation := range s.Observations {
		if observation.FeaturesKnownAt.After(s.AsOf) {
			return fmt.Errorf(
				"%w: observation %d (%s/%s) features known at %s after asOf %s",
				ErrFutureLeak, i, observation.AthleteKey, observation.ClusterKey,
				observation.FeaturesKnownAt.UTC().Format(time.RFC3339Nano),
				s.AsOf.UTC().Format(time.RFC3339Nano))
		}
	}
	return nil
}

func cloneSnapshot(s ProspectiveSnapshot) ProspectiveSnapshot {
	out := s
	out.Observations = make([]Prediction, len(s.Observations))
	for i, observation := range s.Observations {
		out.Observations[i] = clonePrediction(observation)
	}
	return out
}

func clonePrediction(p Prediction) Prediction {
	out := p
	if p.Value != nil {
		value := *p.Value
		out.Value = &value
	}
	if p.WithdrawnAt != nil {
		at := *p.WithdrawnAt
		out.WithdrawnAt = &at
	}
	return out
}

func cloneOutcome(o Outcome) Outcome {
	out := o
	if o.Value != nil {
		value := *o.Value
		out.Value = &value
	}
	return out
}
