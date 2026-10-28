// Package analysis contains deterministic algorithms and their input contracts.
// It has no database, HTTP, filesystem or wall-clock dependency.
package analysis

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
)

const (
	WorkoutAlgorithm    = "workout.bundle"
	StateAlgorithm      = "training_state.bundle"
	DependencyAlgorithm = "dependency.invalidate"
	Version             = "v1"
	StateScope          = "athlete/default"
	WindowDays          = 180
)

type Lease struct {
	ID               string
	DatasetID        string
	Kind             string
	ScopeKey         string
	TargetGeneration int64
	FencingToken     int64
	Payload          []byte
}
type Metric struct {
	Day       string         `json:"day,omitempty"`
	Name      string         `json:"metric"`
	Algorithm string         `json:"algorithm"`
	Version   string         `json:"algorithmVersion"`
	Value     *float64       `json:"value"`
	Unit      string         `json:"unit"`
	Quality   string         `json:"quality"`
	Details   map[string]any `json:"details"`
}
type Snapshot struct {
	Generation   int64         `json:"generation"`
	Revision     int64         `json:"revision"`
	From         time.Time     `json:"from"`
	Through      time.Time     `json:"through"`
	InputVersion string        `json:"inputVersion"`
	Workout      *WorkoutInput `json:"workout,omitempty"`
	State        *StateInput   `json:"state,omitempty"`
	Dependencies []string      `json:"dependencies,omitempty"`
}

func (s Snapshot) Hash() ([]byte, error) {
	s.Revision = 0
	s.Generation = 0
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(b)
	return h[:], nil
}
func Hex(b []byte) string { return hex.EncodeToString(b) }

type Failure struct {
	Code      string
	Retryable bool
}

func (e *Failure) Error() string { return e.Code }
func Invalid() error             { return &Failure{Code: "analysis_invalid_input"} }
func ValidateMetrics(ms []Metric) error {
	seen := map[string]bool{}
	for _, m := range ms {
		key := m.Day + ":" + m.Name
		if _, e := time.Parse("2006-01-02", m.Day); e != nil {
			return Invalid()
		}
		if seen[key] || m.Name == "" || m.Algorithm == "" || m.Version == "" || m.Unit == "" {
			return Invalid()
		}
		seen[key] = true
		if m.Quality != "available" && m.Quality != "partial" && m.Quality != "insufficient" {
			return Invalid()
		}
		if m.Value == nil && m.Quality != "insufficient" {
			return Invalid()
		}
		if m.Value != nil {
			v := *m.Value
			if (strings.HasSuffix(m.Name, "_coverage") || m.Name == "recovery") && (v < 0 || v > 1) {
				return Invalid()
			}
			if m.Name == "readiness" && (v < 0 || v > 100) {
				return Invalid()
			}
			if m.Name != "balance" && v < 0 {
				return Invalid()
			}
		}
		if m.Value != nil && !Finite(*m.Value) {
			return Invalid()
		}
		if _, e := json.Marshal(m.Details); e != nil {
			return Invalid()
		}
	}
	return nil
}
func Finite(v float64) bool  { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func Ptr(v float64) *float64 { return &v }
func Algorithm(kind string) (string, error) {
	switch kind {
	case "analysis.workout":
		return WorkoutAlgorithm, nil
	case "analysis.training_state":
		return StateAlgorithm, nil
	case "analysis.sample", "analysis.series":
		return DependencyAlgorithm, nil
	}
	return "", &Failure{Code: "analysis_unsupported_algorithm"}
}
func ErrorCode(err error) (string, bool) {
	var e *Failure
	if errors.As(err, &e) {
		return e.Code, e.Retryable
	}
	return "analysis_storage_transient", true
}
