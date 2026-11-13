package training

import "time"

// training_plan/v4 is the rolling, adaptive plan. It is selected by a
// training_config/v2 config and parameterised by coach_parameters/v2.
const (
	GeneratorV4        = "training_plan/v4"
	ParameterVersionV2 = "coach_parameters/v2"
	ConfigVersionV2    = "training_config/v2"
)

// SessionRecoveryAlgorithm is the session-recovery model whose readyAfter
// instant keeps hard sessions off days the last runs still load.
const SessionRecoveryAlgorithm = "rai.session_recovery"

// CoachParameters are the frozen constants of training_plan/v4. Every
// threshold of the generator lives here, never inline, so a recalibration is
// a new parameter version, not a silent code change.
type CoachParameters struct {
	Version string `json:"version"`
	// Session floor and session sizes (seconds).
	MinSessionS      float64 `json:"minSessionS"`
	RecoverySessionS float64 `json:"recoverySessionS"`
	MaxEasyS         float64 `json:"maxEasyS"`
	// Weekly volume (seconds).
	MaxWeekS       float64 `json:"maxWeekS"`
	ColdStartWeekS float64 `json:"coldStartWeekS"`
	LowBaseWeekS   float64 `json:"lowBaseWeekS"`
	TwoHardWeekS   float64 `json:"twoHardWeekS"`
	TwoHardDays    int     `json:"twoHardDays"`
	LowFloorFactor float64 `json:"lowFloorFactor"`
	FloorWeeksExit int     `json:"floorWeeksExit"`
	// Long run.
	LongShare    float64 `json:"longShare"`
	LongMinS     float64 `json:"longMinS"`
	LongMaxS     float64 `json:"longMaxFitnessS"`
	LongMax5KS   float64 `json:"longMax5kS"`
	LongMax10KS  float64 `json:"longMax10kS"`
	LongMaxHalfS float64 `json:"longMaxHalfS"`
	LongMaxFullS float64 `json:"longMaxMarathonS"`
	// Week states.
	Progression      float64 `json:"progression"`
	ProgressionCap   float64 `json:"progressionCap"`
	RecoveryFactor   float64 `json:"recoveryFactor"`
	ProgressStreak   int     `json:"progressStreak"`
	CompleteProgress float64 `json:"completeProgress"`
	CompleteHold     float64 `json:"completeHold"`
	ACWRHold         float64 `json:"acwrHold"`
	ACWRRecover      float64 `json:"acwrRecover"`
	ReturnGapDays    int     `json:"returnGapDays"`
	// Day layer.
	ReadinessLow     float64 `json:"readinessLow"`
	ReadinessRest    float64 `json:"readinessRest"`
	ReadinessWeekLow float64 `json:"readinessWeekLow"`
	HardSpacingDays  int     `json:"hardSpacingDays"`
	// In-week rebalancing.
	OverBudget   float64 `json:"overBudget"`
	ExtraVolumeS float64 `json:"extraVolumeS"`
	DoneRatio    float64 `json:"doneRatio"`
	// Load focus (shares of classified load over 28 days).
	FocusLowMin       float64 `json:"focusLowMin"`
	FocusHighMin      float64 `json:"focusHighMin"`
	FocusAnaerobicMin float64 `json:"focusAnaerobicMin"`
	FocusMinRuns      int     `json:"focusMinRuns"`
	// Goal assessment: prediction minus target, relative to prediction.
	GoalOnTrack   float64 `json:"goalOnTrack"`
	GoalAmbitious float64 `json:"goalAmbitious"`
	// Horizon.
	HistoryDays int `json:"historyDays"`
	WindowWeeks int `json:"windowWeeks"`
}

// V2Parameters is coach_parameters/v2. Migration 60 stores the same values;
// the generator refuses any other set.
func V2Parameters() CoachParameters {
	return CoachParameters{
		Version:     ParameterVersionV2,
		MinSessionS: 1800, RecoverySessionS: 1800, MaxEasyS: 4500,
		MaxWeekS: 36000, ColdStartWeekS: 3600, LowBaseWeekS: 5400, TwoHardWeekS: 9000, TwoHardDays: 4,
		LowFloorFactor: 1.3, FloorWeeksExit: 2,
		LongShare: .30, LongMinS: 3600, LongMaxS: 7200, LongMax5KS: 5400, LongMax10KS: 6000, LongMaxHalfS: 7200, LongMaxFullS: 10800,
		Progression: .06, ProgressionCap: .10, RecoveryFactor: .80, ProgressStreak: 3,
		CompleteProgress: .85, CompleteHold: .60, ACWRHold: 1.3, ACWRRecover: 1.5, ReturnGapDays: 10,
		ReadinessLow: 60, ReadinessRest: 40, ReadinessWeekLow: 50, HardSpacingDays: 2,
		OverBudget: 1.10, ExtraVolumeS: 600, DoneRatio: .5,
		FocusLowMin: .70, FocusHighMin: .15, FocusAnaerobicMin: .03, FocusMinRuns: 3,
		GoalOnTrack: .03, GoalAmbitious: .08,
		HistoryDays: 56, WindowWeeks: 4,
	}
}

// WeekSummary is one week of a v4 plan. Past and current weeks are frozen
// once decided (DecidedOn on or after the week start under the same config
// revision); the next week is a preview decided again when it begins; outline
// weeks (race mode only) carry the phase and a projected volume.
type WeekSummary struct {
	Index          int      `json:"index"`
	Start          string   `json:"startDate"`
	Detail         string   `json:"detail"` // past | current | next | outline
	Phase          string   `json:"phase"`  // fitness | pre | base | build | peak | taper | race | post
	State          string   `json:"state"`  // start | progress | hold | recover | regress | return | taper | race | post | untracked | projected
	LevelS         float64  `json:"levelS"`
	BudgetS        float64  `json:"budgetS"`
	PlannedS       float64  `json:"plannedS"`
	ActualS        *float64 `json:"actualS,omitempty"`
	PeakS          float64  `json:"peakS,omitempty"`
	Focus          string   `json:"focus,omitempty"`
	HardFocus      []string `json:"hardFocus,omitempty"`
	Strides        bool     `json:"strides,omitempty"`
	LowBase        bool     `json:"lowBase,omitempty"`
	SingleSession  bool     `json:"singleSession,omitempty"`
	FloorWeeks     int      `json:"floorWeeks,omitempty"`
	ProgressStreak int      `json:"progressStreak,omitempty"`
	DecidedOn      string   `json:"decidedOn,omitempty"`
	ConfigRevision int64    `json:"configRevision,omitempty"`
	Reasons        []string `json:"reasonCodes"`
}

// GoalStatus is the race assessment of a v4 plan with a goal.
type GoalStatus struct {
	Date           string   `json:"date"`
	DistanceM      float64  `json:"distanceM"`
	Category       string   `json:"category"` // 5k | 10k | half | marathon
	Phase          string   `json:"phase"`
	DaysToRace     int      `json:"daysToRace"`
	PrepStart      string   `json:"prepStartDate"`
	TargetTimeS    *float64 `json:"targetTimeS,omitempty"`
	PredictedTimeS *float64 `json:"predictedTimeS,omitempty"`
	GapRatio       *float64 `json:"gapRatio,omitempty"`
	Assessment     string   `json:"assessment"` // on_track | ambitious | out_of_reach | finish | unknown
	RacePaceSPerKM *float64 `json:"racePaceSPerKM,omitempty"`
	Completed      bool     `json:"completed"`
	Reasons        []string `json:"reasonCodes"`
}

func (c Config) validateV2(timezone string) error {
	loc, e := time.LoadLocation(timezone)
	if e != nil {
		return ErrInvalid
	}
	d, e := time.ParseInLocation("2006-01-02", c.StartDate, loc)
	if e != nil || d.Weekday() != time.Monday || c.Weeks != 0 || c.BaselineWeeklyM != nil || c.PreviousLongM != nil {
		return ErrInvalid
	}
	if c.HasGoal() {
		goal, e := time.ParseInLocation("2006-01-02", c.Goal.Date, loc)
		if e != nil || goal.Before(d) || goal.After(d.AddDate(2, 0, 0)) || c.Goal.Type != "running" || !Finite(c.Goal.DistanceM) || c.Goal.DistanceM < 1000 || c.Goal.DistanceM > 42195 {
			return ErrInvalid
		}
		if c.Goal.TargetTimeS != nil && (!Finite(*c.Goal.TargetTimeS) || *c.Goal.TargetTimeS < 60 || *c.Goal.TargetTimeS > 86400) {
			return ErrInvalid
		}
	} else if c.Goal.Date != "" || c.Goal.DistanceM != 0 || c.Goal.TargetTimeS != nil {
		return ErrInvalid
	}
	days := map[int]bool{}
	for _, n := range c.AvailableDays {
		if n < 1 || n > 7 || days[n] {
			return ErrInvalid
		}
		days[n] = true
	}
	if len(days) < 2 || !days[c.LongRunDay] {
		return ErrInvalid
	}
	if c.BaselineWeeklyS != nil && (!Finite(*c.BaselineWeeklyS) || *c.BaselineWeeklyS < 600 || *c.BaselineWeeklyS > 72000) {
		return ErrInvalid
	}
	return nil
}
