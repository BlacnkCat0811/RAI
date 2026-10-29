// Package training owns deterministic coach rules and independent API contracts.
package training

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

const Generator = "training_plan/v1"
const ParameterVersion = "coach_parameters/v1"
const FeedbackVersion = "training_feedback/v1"
const MergeVersion = "training_merge/v2"

type Parameters struct {
	MaxSessionM     float64 `json:"maxSessionM"`
	Version         string  `json:"version"`
	Progression     float64 `json:"progression"`
	Deload          float64 `json:"deload"`
	LongShare       float64 `json:"longShare"`
	LongCeilingM    float64 `json:"longCeilingM"`
	LowReadiness    float64 `json:"lowReadiness"`
	ReadinessFactor float64 `json:"readinessFactor"`
	FeedbackFactor  float64 `json:"feedbackFactor"`
	HardSpacingDays int     `json:"hardSpacingDays"`
	MaxHardPerWeek  int     `json:"maxHardPerWeek"`
}

func V1Parameters() Parameters {
	return Parameters{30000, ParameterVersion, .05, .8, .35, 30000, 50, .8, .9, 2, 1}
}

type Goal struct {
	Type        string   `json:"type"`
	Date        string   `json:"date"`
	DistanceM   float64  `json:"distanceM"`
	TargetTimeS *float64 `json:"targetTimeS,omitempty"`
}
type Config struct {
	StartDate       string   `json:"startDate"`
	Weeks           int      `json:"weeks"`
	Goal            Goal     `json:"goal"`
	AvailableDays   []int    `json:"availableDays"` // ISO Monday=1 ... Sunday=7
	LongRunDay      int      `json:"longRunDay"`
	BaselineWeeklyM *float64 `json:"baselineWeeklyM,omitempty"`
	PreviousLongM   *float64 `json:"previousLongM,omitempty"`
}
type Step struct {
	Kind      string `json:"kind"`
	DurationS int    `json:"durationS"`
	Effort    int    `json:"effort"`
	Repeats   int    `json:"repeats,omitempty"`
	RecoveryS int    `json:"recoveryS,omitempty"`
}
type Session struct {
	ID         string   `json:"sessionID"`
	Slot       int      `json:"slot"`
	Date       string   `json:"date"`
	Type       string   `json:"type"`
	Origin     string   `json:"origin"`
	Status     string   `json:"status"`
	DurationS  float64  `json:"durationS"`
	DistanceM  float64  `json:"distanceM"`
	Effort     int      `json:"effort"`
	TargetHR   *float64 `json:"targetHR,omitempty"`
	PaceSPerKM *float64 `json:"paceSPerKM,omitempty"`
	Notes      string   `json:"notes"`
	Steps      []Step   `json:"steps,omitempty"`
	Reasons    []string `json:"reasonCodes"`
	Modified   bool     `json:"modifiedByUser"`
	Conflicts  []string `json:"conflicts,omitempty"`
}
type Signal struct {
	RunID      string   `json:"runID"`
	Generation int64    `json:"generation"`
	Day        string   `json:"day"`
	Name       string   `json:"metric"`
	Algorithm  string   `json:"algorithm"`
	Version    string   `json:"algorithmVersion"`
	Quality    string   `json:"quality"`
	Value      *float64 `json:"value"`
}
type Workout struct {
	Metrics    []Signal `json:"metrics"`
	ID         string   `json:"workoutID"`
	RunID      string   `json:"runID"`
	Generation int64    `json:"generation"`
	Date       string   `json:"date"`
	DurationS  *float64 `json:"durationS"`
	DistanceM  *float64 `json:"distanceM"`
	Load       *float64 `json:"load"`
}
type Context struct {
	PlanID         string     `json:"planID"`
	Timezone       string     `json:"timezone"`
	AsOf           string     `json:"asOf"`
	ConfigRevision int64      `json:"configRevision"`
	Config         Config     `json:"config"`
	Parameters     Parameters `json:"parameters"`
	Workouts       []Workout  `json:"workouts"`
	Signals        []Signal   `json:"signals"`
	Feedback       []Feedback `json:"feedback"`
}
type Base struct {
	Sessions        []Session `json:"sessions"`
	Recommendations []string  `json:"recommendations"`
}
type Override struct {
	ID        string          `json:"overrideID"`
	SessionID string          `json:"sessionID"`
	Operation string          `json:"operation"`
	Path      string          `json:"path,omitempty"`
	Value     json.RawMessage `json:"value,omitempty"`
	TargetID  string          `json:"targetOverrideID,omitempty"`
	Revision  int64           `json:"revision"`
	Anchor    *Session        `json:"anchor,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
	Status    string          `json:"status"`
}
type Effective struct {
	MergeVersion     string `json:"mergeVersion,omitempty"`
	ID               string `json:"effectiveVersionID"`
	Number           int64  `json:"version"`
	PlanID           string `json:"planID"`
	BaseID           string `json:"baseVersionID"`
	OverrideRevision int64  `json:"overrideRevision"`
	Timezone         string `json:"timezone"`
	Base
}
type Plan struct {
	Active           bool       `json:"active"`
	ID               string     `json:"planID"`
	Timezone         string     `json:"timezone"`
	ConfigRevision   int64      `json:"configRevision"`
	OverrideRevision int64      `json:"overrideRevision"`
	Config           Config     `json:"config"`
	Generation       int64      `json:"generation"`
	Status           string     `json:"status"`
	Effective        *Effective `json:"effective"`
	GenerationState  string     `json:"generationState"`
	LastErrorCode    string     `json:"lastErrorCode,omitempty"`
}
type CreateRequest struct {
	CommandID string `json:"commandID"`
	PlanID    string `json:"planID"`
	Config    Config `json:"config"`
}
type ChangeRequest struct {
	CommandID              string  `json:"commandID"`
	Operation              string  `json:"operation"`
	ExpectedConfigRevision int64   `json:"expectedConfigRevision"`
	Config                 *Config `json:"config,omitempty"`
}
type OverrideRequest struct {
	CommandID                string          `json:"commandID"`
	ExpectedOverrideRevision int64           `json:"expectedOverrideRevision"`
	OverrideID               string          `json:"overrideID"`
	SessionID                string          `json:"sessionID"`
	Operation                string          `json:"operation"`
	Path                     string          `json:"path,omitempty"`
	Value                    json.RawMessage `json:"value,omitempty"`
	TargetID                 string          `json:"targetOverrideID,omitempty"`
	Session                  *ManualSession  `json:"session,omitempty"`
}
type StartRequest struct {
	CommandID   string `json:"commandID"`
	ExecutionID string `json:"executionID"`
	PlanID      string `json:"planID"`
	EffectiveID string `json:"effectiveVersionID"`
	SessionID   string `json:"sessionID"`
	Scheduled   bool   `json:"scheduled"`
}
type ExecutionChange struct {
	CommandID        string `json:"commandID"`
	ExpectedRevision int64  `json:"expectedRevision"`
	Operation        string `json:"operation"`
	WorkoutID        string `json:"workoutID,omitempty"`
	Notes            string `json:"notes,omitempty"`
}
type Execution struct {
	ID          string     `json:"executionID"`
	PlanID      string     `json:"planID"`
	EffectiveID string     `json:"effectiveVersionID"`
	SessionID   string     `json:"sessionID"`
	Revision    int64      `json:"revision"`
	Status      string     `json:"status"`
	Snapshot    Session    `json:"snapshot"`
	StartedAt   *time.Time `json:"startedAt"`
	CompletedAt *time.Time `json:"completedAt"`
	WorkoutID   string     `json:"workoutID,omitempty"`
	Notes       string     `json:"notes"`
	Feedback    []Feedback `json:"feedback"`
}
type Comparison struct {
	Planned float64  `json:"planned"`
	Actual  *float64 `json:"actual"`
	Delta   *float64 `json:"delta"`
	Ratio   *float64 `json:"ratio"`
}
type Feedback struct {
	ID                string     `json:"feedbackID,omitempty"`
	ExecutionID       string     `json:"executionID"`
	ExecutionRevision int64      `json:"executionRevision"`
	Version           string     `json:"algorithmVersion"`
	SourceRunID       string     `json:"sourceRunID,omitempty"`
	Status            string     `json:"status"`
	Quality           string     `json:"quality"`
	Date              string     `json:"date"`
	Duration          Comparison `json:"duration"`
	Distance          Comparison `json:"distance"`
	ActualLoad        *float64   `json:"actualLoad"`
	Reasons           []string   `json:"reasonCodes"`
}
type Audit struct {
	Effective Effective  `json:"effective"`
	Base      Base       `json:"base"`
	Context   Context    `json:"context"`
	Overrides []Override `json:"overrides"`
}
type History struct {
	Versions []VersionSummary `json:"versions"`
	Next     int64            `json:"next"`
}
type LegacyHistory struct {
	Versions []Effective `json:"versions"`
	Next     int64       `json:"next"`
}

// Product reads have byte budgets in addition to item counts. Immutable array
// ordinals and version/revision numbers are stable cursors, never SQL offsets.
const PageBudget = 256 << 10
const SnapshotBudget = 1 << 20
const AuditEntryBudget = 32 << 10

type VersionSummary struct {
	ID               string    `json:"effectiveVersionID"`
	Number           int64     `json:"version"`
	PlanID           string    `json:"planID"`
	BaseID           string    `json:"baseVersionID"`
	OverrideRevision int64     `json:"overrideRevision"`
	MergeVersion     string    `json:"mergeVersion"`
	Timezone         string    `json:"timezone"`
	CreatedAt        time.Time `json:"createdAt"`
}
type AuditPage struct {
	Version   VersionSummary `json:"version"`
	Overrides []Override     `json:"overrides"`
	Next      int64          `json:"next"`
}
type DetailPage struct {
	Version  VersionSummary    `json:"version"`
	Section  string            `json:"section"`
	Metadata json.RawMessage   `json:"metadata"`
	Items    []json.RawMessage `json:"items"`
	Next     int64             `json:"next"`
}
type Input struct {
	Generation int64
	Plan       Plan
	Context    Context
	Executions []Execution
	Workouts   map[string]Workout
}

var ErrInsufficient = errors.New("insufficient_context")
var ErrInvalid = errors.New("invalid_training_input")

func Finite(v float64) bool  { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func Ptr(v float64) *float64 { return &v }
func Hash(v any) ([]byte, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	h := sha256.Sum256(b)
	return h[:], nil
}
func StableID(plan string, slot int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("training_session/v1:%s:%d", plan, slot)))
	h[6] = (h[6] & 15) | 80
	h[8] = (h[8] & 63) | 128
	s := hex.EncodeToString(h[:16])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}
func (c Config) Validate(timezone string) error {
	loc, e := time.LoadLocation(timezone)
	if e != nil {
		return ErrInvalid
	}
	d, e := time.ParseInLocation("2006-01-02", c.StartDate, loc)
	if e != nil || d.Weekday() != time.Monday || c.Weeks < 4 || c.Weeks > 16 {
		return ErrInvalid
	}
	goal, e := time.ParseInLocation("2006-01-02", c.Goal.Date, loc)
	if e != nil || goal.Before(d) || !goal.Before(d.AddDate(0, 0, c.Weeks*7)) || c.Goal.Type != "running" || !Finite(c.Goal.DistanceM) || c.Goal.DistanceM < 1000 || c.Goal.DistanceM > 42195 {
		return ErrInvalid
	}
	if c.Goal.TargetTimeS != nil && (!Finite(*c.Goal.TargetTimeS) || *c.Goal.TargetTimeS < 60 || *c.Goal.TargetTimeS > 86400) {
		return ErrInvalid
	}
	days := map[int]bool{}
	for _, n := range c.AvailableDays {
		if n < 1 || n > 7 || days[n] {
			return ErrInvalid
		}
		days[n] = true
	}
	if len(days) < 2 || len(days) > 6 || !days[c.LongRunDay] {
		return ErrInvalid
	}
	if c.BaselineWeeklyM != nil && (!Finite(*c.BaselineWeeklyM) || *c.BaselineWeeklyM < 3000 || *c.BaselineWeeklyM > 160000) {
		return ErrInvalid
	}
	if c.PreviousLongM != nil && (!Finite(*c.PreviousLongM) || *c.PreviousLongM < 1000 || *c.PreviousLongM > 50000 || (c.BaselineWeeklyM != nil && *c.PreviousLongM > *c.BaselineWeeklyM)) {
		return ErrInvalid
	}
	return nil
}
func ValidateSession(s Session) error {
	if _, e := time.Parse("2006-01-02", s.Date); e != nil {
		return ErrInvalid
	}
	switch s.Type {
	case "REST", "EASY", "LONG", "TEMPO", "INTERVAL", "RECOVERY":
	default:
		return ErrInvalid
	}
	if !Finite(s.DurationS) || s.DurationS < 0 || s.DurationS > 21600 || !Finite(s.DistanceM) || s.DistanceM < 0 || s.DistanceM > 100000 || s.Effort < 0 || s.Effort > 10 || len(s.Notes) > 2000 {
		return ErrInvalid
	}
	if s.Type != "REST" && s.DurationS == 0 && s.DistanceM == 0 {
		return ErrInvalid
	}
	if s.Type == "REST" && (s.DurationS != 0 || s.DistanceM != 0 || s.Effort != 0) {
		return ErrInvalid
	}
	for _, v := range []struct {
		p      *float64
		lo, hi float64
	}{{s.TargetHR, 30, 240}, {s.PaceSPerKM, 120, 1800}} {
		if v.p != nil && (!Finite(*v.p) || *v.p < v.lo || *v.p > v.hi) {
			return ErrInvalid
		}
	}
	if len(s.Steps) > 8 {
		return ErrInvalid
	}
	totalStepS := 0
	if len(s.Steps) > 0 && s.Type != "INTERVAL" {
		return ErrInvalid
	}
	for _, v := range s.Steps {
		if v.Kind != "warmup" && v.Kind != "work" && v.Kind != "repeat" && v.Kind != "cooldown" {
			return ErrInvalid
		}
		if v.DurationS < 1 || v.DurationS > 7200 || v.Repeats < 0 || v.Repeats > 20 || v.RecoveryS < 0 || v.RecoveryS > 3600 || v.Effort < 0 || v.Effort > 10 {
			return ErrInvalid
		}
		if v.Kind == "repeat" {
			if v.Repeats < 1 || v.RecoveryS < 1 {
				return ErrInvalid
			}
			totalStepS += v.Repeats * (v.DurationS + v.RecoveryS)
		} else {
			if v.Repeats != 0 || v.RecoveryS != 0 {
				return ErrInvalid
			}
			totalStepS += v.DurationS
		}
	}
	if len(s.Steps) > 0 && s.DurationS != float64(totalStepS) {
		return ErrInvalid
	}
	return nil
}
func SortSessions(s []Session) {
	sort.Slice(s, func(i, j int) bool {
		if s[i].Date == s[j].Date {
			return s[i].ID < s[j].ID
		}
		return s[i].Date < s[j].Date
	})
}

// ManualSession is a write DTO; generated lineage and explanation fields cannot
// be supplied or silently discarded by the add-session API.
type ManualSession struct {
	ID         string   `json:"sessionID"`
	Date       string   `json:"date"`
	Type       string   `json:"type"`
	DurationS  float64  `json:"durationS"`
	DistanceM  float64  `json:"distanceM"`
	Effort     int      `json:"effort"`
	TargetHR   *float64 `json:"targetHR,omitempty"`
	PaceSPerKM *float64 `json:"paceSPerKM,omitempty"`
	Notes      string   `json:"notes"`
	Steps      []Step   `json:"steps,omitempty"`
}

func (m ManualSession) Prescription() Session {
	return Session{ID: m.ID, Date: m.Date, Type: m.Type, DurationS: m.DurationS, DistanceM: m.DistanceM, Effort: m.Effort, TargetHR: m.TargetHR, PaceSPerKM: m.PaceSPerKM, Notes: m.Notes, Steps: m.Steps}
}
