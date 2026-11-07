package diagnostics

import (
	"math"
	"testing"
)

const testDayMicros int64 = 86_400_000_000

func f64(v float64) *float64 { return &v }

func closeTo(t *testing.T, got, want, tolerance float64) {
	t.Helper()
	if math.Abs(got-want) > tolerance {
		t.Fatalf("got %.15g, want %.15g (tolerance %g)", got, want, tolerance)
	}
}

func requireStatus(t *testing.T, got Status, want Status) {
	t.Helper()
	if got != want {
		t.Fatalf("status = %q, want %q; reasons=%v", got, want, got)
	}
}

func requireReason(t *testing.T, reasons ReasonCodes, want ReasonCode) {
	t.Helper()
	if !reasons.Has(want) {
		t.Fatalf("missing reason %q in %v", want, reasons)
	}
}

func knownSample(start, end, speed, hr float64) DurabilitySample {
	return DurabilitySample{
		StartSeconds: start,
		EndSeconds:   end,
		SpeedMPS:     f64(speed),
		HeartRateBPM: f64(hr),
		SourceEpoch:  "healthkit",
		Terrain:      "road",
		Environment:  Environment{TemperatureC: f64(20)},
		KnownAt:      1_700_000_000_000_000,
	}
}

func TestIdentitiesAreIndependentAndExperimental(t *testing.T) {
	identities := []Identity{
		WorkoutDurabilityIdentity(),
		AerobicEfficiencyIdentity(),
		CriticalSpeedIdentity(),
		LoadSettlingIdentity(),
	}
	want := []string{
		"rai.workout_durability/v1",
		"rai.aerobic_efficiency/v1",
		"rai.critical_speed/v1",
		"rai.load_settling/v1",
	}
	seen := map[string]bool{}
	for i, identity := range identities {
		if identity.ID != want[i] {
			t.Fatalf("identity %d = %q, want %q", i, identity.ID, want[i])
		}
		if !identity.Experimental {
			t.Fatalf("identity %q is not experimental", identity.ID)
		}
		if seen[identity.ID] {
			t.Fatalf("duplicate identity %q", identity.ID)
		}
		seen[identity.ID] = true
	}
}

func TestCalculateDurabilityOracle(t *testing.T) {
	// Warm-up 0..600 is excluded. The analysed halves are 600..2100 and
	// 2100..3600, with speeds 3.0 and 2.95 m/s and HR 150 and 155 bpm.
	samples := []DurabilitySample{
		knownSample(0, 600, 3.0, 140),
		knownSample(600, 2100, 3.0, 150),
		knownSample(2100, 3600, 2.95, 155),
	}
	result, err := CalculateDurability(DurabilityInput{WorkoutID: "w-durability", Samples: samples})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, result.Status, StatusOK)
	if result.EF1 == nil || result.EF2 == nil || result.Decoupling == nil || result.PaceDecay == nil {
		t.Fatalf("missing metrics: %+v", result)
	}
	closeTo(t, *result.EF1, 1.2, 1e-12)
	closeTo(t, *result.EF2, 177.0/155.0, 1e-12)
	closeTo(t, *result.Decoupling, 0.04838709677419356, 1e-12)
	closeTo(t, *result.PaceDecay, 0.01666666666666672, 1e-12)
	if result.FirstHalf == nil || result.SecondHalf == nil {
		t.Fatal("missing halves")
	}
	closeTo(t, *result.FirstHalf.MeanSpeedMPS, 3.0, 1e-12)
	closeTo(t, *result.SecondHalf.MeanSpeedMPS, 2.95, 1e-12)
	closeTo(t, result.SpeedSupport, 1.0, 1e-12)
	closeTo(t, result.HRSupport, 1.0, 1e-12)
}

func TestDurabilityPaceShiftSuppressesDecoupling(t *testing.T) {
	samples := []DurabilitySample{
		knownSample(0, 600, 3.0, 140),
		knownSample(600, 2100, 3.0, 150),
		knownSample(2100, 3600, 2.8, 150),
	}
	result, err := CalculateDurability(DurabilityInput{WorkoutID: "w-shift", Samples: samples})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, result.Status, StatusPartial)
	if result.Decoupling != nil {
		t.Fatalf("decoupling = %v, want nil", *result.Decoupling)
	}
	if result.EF1 == nil || result.EF2 == nil {
		t.Fatal("EF values should remain available")
	}
	if result.PaceDecay == nil {
		t.Fatal("pace decay should remain available")
	}
	closeTo(t, *result.PaceDecay, 1-2.8/3.0, 1e-12)
	requireReason(t, result.Reasons, ReasonPaceShiftConfoundsDrift)
}

func TestDurabilityMissingHRKeepsPaceDecay(t *testing.T) {
	samples := []DurabilitySample{
		{StartSeconds: 0, EndSeconds: 600, SpeedMPS: f64(3.0), SourceEpoch: "healthkit", Terrain: "road"},
		{StartSeconds: 600, EndSeconds: 2100, SpeedMPS: f64(3.0), SourceEpoch: "healthkit", Terrain: "road"},
		{StartSeconds: 2100, EndSeconds: 3600, SpeedMPS: f64(2.9), SourceEpoch: "healthkit", Terrain: "road"},
	}
	result, err := CalculateDurability(DurabilityInput{WorkoutID: "w-no-hr", Samples: samples})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, result.Status, StatusPartial)
	requireReason(t, result.Reasons, ReasonMissingHR)
	if result.EF1 != nil || result.EF2 != nil || result.Decoupling != nil {
		t.Fatal("HR-based metrics must be absent")
	}
	if result.PaceDecay == nil {
		t.Fatal("pace decay must remain available without HR")
	}
	closeTo(t, *result.PaceDecay, 1-2.9/3.0, 1e-12)
}

func TestDurabilityDoesNotSplicePauses(t *testing.T) {
	samples := []DurabilitySample{
		knownSample(0, 1800, 3.0, 150),
		{StartSeconds: 1800, EndSeconds: 1801, Paused: true, SourceEpoch: "healthkit", Terrain: "road"},
		knownSample(1801, 3601, 3.0, 150),
	}
	result, err := CalculateDurability(DurabilityInput{WorkoutID: "w-pause", Samples: samples})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, result.Status, StatusInsufficient)
	requireReason(t, result.Reasons, ReasonDurationTooShort)
	closeTo(t, result.LongestContinuousSeconds, 1800, 1e-12)
}

func TestCalculateDurabilityRejectsBadInput(t *testing.T) {
	if _, err := CalculateDurability(DurabilityInput{}); err != ErrEmptyInput {
		t.Fatalf("empty error = %v, want %v", err, ErrEmptyInput)
	}
	bad := []DurabilitySample{{StartSeconds: 0, EndSeconds: 10, SpeedMPS: f64(math.NaN())}}
	if _, err := CalculateDurability(DurabilityInput{Samples: bad}); err != ErrNonFinite {
		t.Fatalf("non-finite error = %v, want %v", err, ErrNonFinite)
	}
}

func durabilityRun(id string, at int64, ef, decoupling, pace, speed float64) DurabilityResult {
	return DurabilityResult{
		Header:          Header{Status: StatusOK},
		WorkoutID:       id,
		KnownAt:         at,
		SourceEpoch:     "healthkit",
		Terrain:         "road",
		TemperatureC:    f64(20),
		DurationSeconds: 3600,
		MeanSpeedMPS:    f64(speed),
		EF1:             f64(ef),
		Decoupling:      f64(decoupling),
		PaceDecay:       f64(pace),
	}
}

func TestDurabilityTrendAtLeastThreeComparableRuns(t *testing.T) {
	base := int64(1_700_000_000_000_000)
	history := []DurabilityResult{
		durabilityRun("a", base, 1.20, 0.040, 0.010, 3.00),
		durabilityRun("b", base+7*testDayMicros, 1.22, 0.030, 0.008, 3.05),
		durabilityRun("c", base+14*testDayMicros, 1.24, 0.020, 0.006, 3.10),
	}
	result, err := CalculateDurabilityTrend(history, base+14*testDayMicros)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, result.Status, StatusOK)
	if result.ComparableRuns != 3 {
		t.Fatalf("comparable runs = %d, want 3", result.ComparableRuns)
	}
	if result.MedianEF == nil || result.MedianDecoupling == nil || result.MedianPaceDecay == nil {
		t.Fatal("missing medians")
	}
	closeTo(t, *result.MedianEF, 1.22, 1e-12)
	closeTo(t, *result.MedianDecoupling, 0.030, 1e-12)
	closeTo(t, *result.MedianPaceDecay, 0.008, 1e-12)
	if result.EFTrend != TrendIncreasing || result.DecouplingTrend != TrendDecreasing || result.PaceDecayTrend != TrendDecreasing {
		t.Fatalf("unexpected trends: %q %q %q", result.EFTrend, result.DecouplingTrend, result.PaceDecayTrend)
	}
}

func TestDurabilityTrendCollectsWithFewerThanThree(t *testing.T) {
	base := int64(1_700_000_000_000_000)
	history := []DurabilityResult{
		durabilityRun("a", base, 1.20, 0.040, 0.010, 3.00),
		durabilityRun("b", base+7*testDayMicros, 1.22, 0.030, 0.008, 3.05),
	}
	result, err := CalculateDurabilityTrend(history, base+14*testDayMicros)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, result.Status, StatusCollecting)
	requireReason(t, result.Reasons, ReasonInsufficientRuns)
	if result.MedianEF != nil {
		t.Fatal("median must be absent while collecting")
	}
}

func TestDurabilityTrendRejectsDifferentContext(t *testing.T) {
	base := int64(1_700_000_000_000_000)
	history := []DurabilityResult{
		durabilityRun("a", base, 1.20, 0.040, 0.010, 3.00),
		durabilityRun("b", base+7*testDayMicros, 1.22, 0.030, 0.008, 3.05),
	}
	// Replace the third run's terrain so the latest reference has only two
	// comparable predecessors.
	third := durabilityRun("c", base+14*testDayMicros, 1.24, 0.020, 0.006, 3.10)
	third.Terrain = "trail"
	history = append(history, third)
	result, err := CalculateDurabilityTrend(history, base+14*testDayMicros)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, result.Status, StatusCollecting)
	if result.ComparableRuns != 1 {
		t.Fatalf("comparable runs = %d, want 1", result.ComparableRuns)
	}
}

func makeCriticalEffort(id string, seconds, distance float64, at int64) CriticalSpeedEffort {
	return CriticalSpeedEffort{
		WorkoutID:       id,
		DurationSeconds: seconds,
		DistanceMeters:  distance,
		MeanSpeedMPS:    distance / seconds,
		Quality:         1,
		SourceEpoch:     "healthkit",
		KnownAt:         at,
	}
}

func TestCriticalSpeedExactWLS(t *testing.T) {
	// D = 4.0*t + 200 exactly at 180, 300 and 600 seconds.
	base := int64(1_700_000_000_000_000)
	workouts := []CriticalSpeedEffort{
		makeCriticalEffort("w3", 180, 920, base),
		makeCriticalEffort("w5", 300, 1400, base+1_000_000),
		makeCriticalEffort("w10", 600, 2600, base+2_000_000),
	}
	result, err := CalculateCriticalSpeed(CriticalSpeedInput{AsOf: base + 10*testDayMicros, Workouts: workouts})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, result.Status, StatusOK)
	if result.CSMPS == nil || result.DPrimeMeters == nil || result.R2 == nil || result.RelativeRMSE == nil {
		t.Fatalf("missing fit values: %+v", result)
	}
	closeTo(t, *result.CSMPS, 4.0, 1e-9)
	closeTo(t, *result.DPrimeMeters, 200.0, 1e-9)
	closeTo(t, *result.R2, 1.0, 1e-12)
	closeTo(t, *result.RelativeRMSE, 0.0, 1e-12)
	if result.WorkoutCount != 3 || result.DistinctDurations != 3 {
		t.Fatalf("counts = %d/%d, want 3/3", result.WorkoutCount, result.DistinctDurations)
	}
	if result.LOOMaxCSChange == nil {
		t.Fatal("missing LOO change")
	}
	closeTo(t, *result.LOOMaxCSChange, 0.0, 1e-9)
}

func TestCriticalSpeedSingleWorkoutSplitDoesNotAddIndependence(t *testing.T) {
	base := int64(1_700_000_000_000_000)
	// All three durations belong to exactly one workout.
	workouts := []CriticalSpeedEffort{
		makeCriticalEffort("only-workout", 180, 920, base),
		makeCriticalEffort("only-workout", 300, 1400, base+1_000_000),
		makeCriticalEffort("only-workout", 600, 2600, base+2_000_000),
	}
	result, err := CalculateCriticalSpeed(CriticalSpeedInput{AsOf: base + 10*testDayMicros, Workouts: workouts})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, result.Status, StatusCollecting)
	requireReason(t, result.Reasons, ReasonTooFewWorkouts)
	if result.WorkoutCount != 1 {
		t.Fatalf("workout count = %d, want 1", result.WorkoutCount)
	}
	if result.DistinctDurations != 3 {
		t.Fatalf("distinct durations = %d, want 3", result.DistinctDurations)
	}
	if result.CSMPS != nil || result.DPrimeMeters != nil {
		t.Fatal("no usable estimate may be emitted")
	}
}

func TestCriticalSpeedLOOFailure(t *testing.T) {
	base := int64(1_700_000_000_000_000)
	// The three-effort fit is plausible, but dropping the 10-minute point
	// changes CS by more than 5% (the remaining pair has CS=3.75).
	workouts := []CriticalSpeedEffort{
		makeCriticalEffort("a", 180, 850, base),
		makeCriticalEffort("b", 300, 1300, base+1_000_000),
		makeCriticalEffort("c", 600, 2520, base+2_000_000),
	}
	result, err := CalculateCriticalSpeed(CriticalSpeedInput{AsOf: base + 10*testDayMicros, Workouts: workouts})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, result.Status, StatusUnstable)
	requireReason(t, result.Reasons, ReasonLOOCSUnstable)
	if result.CSMPS != nil || result.DPrimeMeters != nil || result.R2 != nil || result.RelativeRMSE != nil {
		t.Fatal("unstable fit must not emit usable values")
	}
}

func TestCriticalSpeedRejectsBadInput(t *testing.T) {
	if _, err := CalculateCriticalSpeed(CriticalSpeedInput{}); err != ErrEmptyInput {
		t.Fatalf("empty error = %v, want %v", err, ErrEmptyInput)
	}
	bad := []CriticalSpeedEffort{{DurationSeconds: 300, DistanceMeters: math.Inf(1), Quality: 1}}
	if _, err := CalculateCriticalSpeed(CriticalSpeedInput{Workouts: bad}); err != ErrNonFinite {
		t.Fatalf("non-finite error = %v, want %v", err, ErrNonFinite)
	}
	if _, err := CalculateCriticalSpeed(CriticalSpeedInput{AsOf: -1, Workouts: []CriticalSpeedEffort{makeCriticalEffort("a", 180, 920, 1)}}); err != ErrInvalidInput {
		t.Fatalf("negative as-of error = %v, want %v", err, ErrInvalidInput)
	}
}

func TestAerobicEfficiencyMetersPerBeatWithoutReference(t *testing.T) {
	// No HRmax, resting HR or other reference is present in the input.
	// 3.0 m/s = 180 m/min; 180/150 = 1.2 m/beat.
	result, err := CalculateAerobicEfficiency(AerobicEfficiencyInput{
		WorkoutID: "aerobic-1",
		Supports: []SteadySupport{{
			SpeedMPS:          f64(3.0),
			HeartRateBPM:      f64(150),
			ValidSeconds:      600,
			HRSupportFraction: 1.0,
			SourceEpoch:       "healthkit",
			Terrain:           "road",
			KnownAt:           1_700_000_000_000_000,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, result.Status, StatusOK)
	if result.EfficiencyMPerBeat == nil || result.SpeedMPerMin == nil || result.HeartRateBPM == nil {
		t.Fatalf("missing efficiency values: %+v", result)
	}
	closeTo(t, *result.SpeedMPerMin, 180, 1e-12)
	closeTo(t, *result.HeartRateBPM, 150, 1e-12)
	closeTo(t, *result.EfficiencyMPerBeat, 1.2, 1e-12)
}

func TestAerobicEfficiencyGates(t *testing.T) {
	baseSupport := SteadySupport{
		SpeedMPS:          f64(3.0),
		HeartRateBPM:      f64(150),
		ValidSeconds:      600,
		HRSupportFraction: 1.0,
		SourceEpoch:       "healthkit",
		Terrain:           "road",
	}
	short := baseSupport
	short.ValidSeconds = 299
	result, err := CalculateAerobicEfficiency(AerobicEfficiencyInput{Supports: []SteadySupport{short}})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, result.Status, StatusInsufficient)
	requireReason(t, result.Reasons, ReasonSteadyTooShort)

	lowHR := baseSupport
	lowHR.HRSupportFraction = 0.80
	result, err = CalculateAerobicEfficiency(AerobicEfficiencyInput{Supports: []SteadySupport{lowHR}})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, result.Status, StatusInsufficient)
	requireReason(t, result.Reasons, ReasonHRSupportFraction)

	noHR := baseSupport
	noHR.HeartRateBPM = nil
	result, err = CalculateAerobicEfficiency(AerobicEfficiencyInput{Supports: []SteadySupport{noHR}})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, result.Status, StatusInsufficient)
	requireReason(t, result.Reasons, ReasonMissingHR)
}

func TestAerobicEfficiencyRejectsBadInput(t *testing.T) {
	if _, err := CalculateAerobicEfficiency(AerobicEfficiencyInput{}); err != ErrEmptyInput {
		t.Fatalf("empty error = %v, want %v", err, ErrEmptyInput)
	}
	if _, err := CalculateAerobicEfficiency(AerobicEfficiencyInput{Supports: []SteadySupport{{SpeedMPS: f64(math.NaN()), ValidSeconds: 600, HRSupportFraction: 1}}}); err != ErrNonFinite {
		t.Fatalf("non-finite error = %v, want %v", err, ErrNonFinite)
	}
}

func aerobicRun(id string, at int64, value float64, terrain string) AerobicEfficiencyResult {
	return AerobicEfficiencyResult{
		Header:             Header{Status: StatusOK},
		WorkoutID:          id,
		KnownAt:            at,
		SourceEpoch:        "healthkit",
		Terrain:            terrain,
		EfficiencyMPerBeat: f64(value),
	}
}

func TestAerobicEfficiencyTrendSameContext(t *testing.T) {
	base := int64(1_700_000_000_000_000)
	history := []AerobicEfficiencyResult{
		aerobicRun("a", base, 1.20, "road"),
		aerobicRun("b", base+7*testDayMicros, 1.25, "road"),
		aerobicRun("c", base+14*testDayMicros, 1.30, "road"),
	}
	result, err := CalculateAerobicEfficiencyTrend(history, base+14*testDayMicros)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, result.Status, StatusOK)
	if result.ComparableRuns != 3 || result.MedianEfficiency == nil {
		t.Fatalf("unexpected trend result: %+v", result)
	}
	closeTo(t, *result.MedianEfficiency, 1.25, 1e-12)
	if result.EfficiencyTrend != TrendIncreasing {
		t.Fatalf("trend = %q, want increasing", result.EfficiencyTrend)
	}
}

func TestAerobicEfficiencyTrendRequiresContext(t *testing.T) {
	base := int64(1_700_000_000_000_000)
	history := []AerobicEfficiencyResult{
		aerobicRun("a", base, 1.20, "trail"),
		aerobicRun("b", base+7*testDayMicros, 1.25, "road"),
		aerobicRun("c", base+14*testDayMicros, 1.30, "road"),
	}
	result, err := CalculateAerobicEfficiencyTrend(history, base+14*testDayMicros)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, result.Status, StatusCollecting)
	if result.ComparableRuns != 2 {
		t.Fatalf("comparable runs = %d, want 2", result.ComparableRuns)
	}
}

func TestSettlingBucketBoundaries(t *testing.T) {
	cases := []struct {
		hours float64
		want  string
	}{
		{0, "<12"},
		{11.999999, "<12"},
		{12, "[12,24)"},
		{23.999999, "[12,24)"},
		{24, "[24,36)"},
		{35.999999, "[24,36)"},
		{36, "[36,48)"},
		{47.999999, "[36,48)"},
		{48, ">48"},
		{100, ">48"},
		{math.NaN(), ""},
		{math.Inf(1), ""},
		{-1, ""},
	}
	for _, tc := range cases {
		if got := SettlingBucket(tc.hours); got != tc.want {
			t.Fatalf("SettlingBucket(%v) = %q, want %q", tc.hours, got, tc.want)
		}
	}
}

func TestSettlingWindowFormulaAndGates(t *testing.T) {
	// h = 36*ln(100/(0.25*100)) = 36*ln(4) = 49.9065970003...
	result, err := SettlingWindow(100, 100, 90, true, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, result.Status, StatusOK)
	if result.Hours == nil {
		t.Fatal("missing hours")
	}
	closeTo(t, *result.Hours, 36*math.Log(4), 1e-12)
	if result.Bucket != ">48" {
		t.Fatalf("bucket = %q, want >48", result.Bucket)
	}

	// D=0 is defined as zero hours even with a zero baseline; the bucket still
	// requires a valid baseline.
	zero, err := SettlingWindow(0, 0, 90, true, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	if zero.Hours == nil || *zero.Hours != 0 {
		t.Fatalf("D=0 hours = %v, want 0", zero.Hours)
	}
	requireStatus(t, zero.Status, StatusPartial)
	requireReason(t, zero.Reasons, ReasonBaselineMissing)
	if zero.Bucket != "" {
		t.Fatalf("D=0 missing baseline bucket = %q, want empty", zero.Bucket)
	}

	zeroValid, err := SettlingWindow(0, 100, 90, true, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, zeroValid.Status, StatusOK)
	if zeroValid.Bucket != "<12" {
		t.Fatalf("D=0 bucket = %q, want <12", zeroValid.Bucket)
	}

	lowScore, err := SettlingWindow(100, 100, 84.9, true, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, lowScore.Status, StatusPartial)
	requireReason(t, lowScore.Reasons, ReasonScoreBelowGate)
	if lowScore.Bucket != "" || lowScore.Hours == nil {
		t.Fatalf("low score = %+v, want hours without bucket", lowScore)
	}

	noBaseline, err := SettlingWindow(100, 0, 90, true, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, noBaseline.Status, StatusInsufficient)
	requireReason(t, noBaseline.Reasons, ReasonBaselineMissing)
	if noBaseline.Hours != nil || noBaseline.Bucket != "" {
		t.Fatal("missing baseline must not emit hours or a bucket")
	}

	incomplete, err := SettlingWindow(100, 100, 90, false, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, incomplete.Status, StatusPartial)
	requireReason(t, incomplete.Reasons, ReasonCoverageIncomplete)
	if incomplete.Bucket != "" {
		t.Fatal("incomplete coverage must not emit a bucket")
	}
}

func TestSettlingWindowRejectsInvalidNumbers(t *testing.T) {
	if _, err := SettlingWindow(math.NaN(), 100, 90, true, 0.95); err != ErrNonFinite {
		t.Fatalf("NaN error = %v, want %v", err, ErrNonFinite)
	}
	if _, err := SettlingWindow(100, math.Inf(1), 90, true, 0.95); err != ErrNonFinite {
		t.Fatalf("Inf error = %v, want %v", err, ErrNonFinite)
	}
	if _, err := SettlingWindow(-1, 100, 90, true, 0.95); err != ErrNegativeValue {
		t.Fatalf("negative load error = %v, want %v", err, ErrNegativeValue)
	}
	if _, err := SettlingWindow(100, -1, 90, true, 0.95); err != ErrNegativeValue {
		t.Fatalf("negative baseline error = %v, want %v", err, ErrNegativeValue)
	}
	if _, err := SettlingWindow(100, 100, 90, true, -0.1); err != ErrNegativeValue {
		t.Fatalf("negative confidence error = %v, want %v", err, ErrNegativeValue)
	}
}

func TestTrendAndWrapperFunctionsHandleEmptyInput(t *testing.T) {
	if _, err := CalculateCriticalSpeedFromEfforts(nil); err != ErrEmptyInput {
		t.Fatalf("CS wrapper empty error = %v, want %v", err, ErrEmptyInput)
	}

	durabilityTrend, err := CalculateDurabilityTrend(nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, durabilityTrend.Status, StatusCollecting)
	if _, err := CalculateDurabilityTrend(nil, 0); err != ErrInvalidInput {
		t.Fatalf("durability trend anchor error = %v, want %v", err, ErrInvalidInput)
	}

	aerobicTrend, err := CalculateAerobicEfficiencyTrend(nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, aerobicTrend.Status, StatusCollecting)
	if _, err := CalculateAerobicEfficiencyTrend(nil, 0); err != ErrInvalidInput {
		t.Fatalf("aerobic trend anchor error = %v, want %v", err, ErrInvalidInput)
	}
}

func TestDurabilityTrendKeepsPaceDecayWhenHRIsMissing(t *testing.T) {
	base := int64(1_700_000_000_000_000)
	history := []DurabilityResult{
		durabilityRun("a", base, 1.20, 0.040, 0.010, 3.00),
		durabilityRun("b", base+7*testDayMicros, 1.22, 0.030, 0.008, 3.05),
		durabilityRun("c", base+14*testDayMicros, 1.24, 0.020, 0.006, 3.10),
	}
	for i := range history {
		history[i].EF1 = nil
		history[i].Decoupling = nil
	}
	result, err := CalculateDurabilityTrend(history, base+14*testDayMicros)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, result.Status, StatusPartial)
	requireReason(t, result.Reasons, ReasonDecouplingPartial)
	if result.MedianEF != nil || result.MedianDecoupling != nil {
		t.Fatal("HR-dependent medians must be absent")
	}
	if result.MedianPaceDecay == nil {
		t.Fatal("pace decay median should remain available")
	}
	closeTo(t, *result.MedianPaceDecay, 0.008, 1e-12)
}
