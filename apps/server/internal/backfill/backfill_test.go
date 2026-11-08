package backfill

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func baseRequest() Request {
	return Request{
		DatasetID:   "ds_1",
		ManifestSHA: strings.Repeat("a", 64),
		StackID:     "stack_1",
		From:        "2026-01-01",
		Through:     "2026-01-31",
		AsOf:        "2026-02-01",
		View:        ViewRevised,
		Operation:   OperationBackfill,
		Actor:       "tester",
		BlockDays:   31,
	}
}

func testDay(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(DayLayout, value)
	if err != nil {
		t.Fatalf("bad test day %q: %v", value, err)
	}
	return parsed
}

func testAdd(t *testing.T, value string, days int) string {
	t.Helper()
	return testDay(t, value).AddDate(0, 0, days).Format(DayLayout)
}

func testSpanDays(t *testing.T, from, through string) int {
	t.Helper()
	return int(testDay(t, through).Sub(testDay(t, from))/(24*time.Hour)) + 1
}

func TestValidateRangeBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Request)
		wantErr bool
	}{
		{name: "one day", mutate: func(r *Request) { r.From, r.Through = "2026-01-01", "2026-01-01" }},
		{name: "366 days", mutate: func(r *Request) { r.From, r.Through = "2025-01-01", "2026-01-01" }},
		{name: "367 days rejected", mutate: func(r *Request) { r.From, r.Through = "2024-01-01", "2025-01-01" }, wantErr: true},
		{name: "zero days rejected", mutate: func(r *Request) { r.From, r.Through = "2026-01-02", "2026-01-01" }, wantErr: true},
		{name: "block zero rejected", mutate: func(r *Request) { r.BlockDays = 0 }, wantErr: true},
		{name: "block 32 rejected", mutate: func(r *Request) { r.BlockDays = 32 }, wantErr: true},
		{name: "block one accepted", mutate: func(r *Request) { r.BlockDays = 1 }},
		{name: "bad view", mutate: func(r *Request) { r.View = "latest" }, wantErr: true},
		{name: "bad operation", mutate: func(r *Request) { r.Operation = "delete_everything" }, wantErr: true},
		{name: "malformed from", mutate: func(r *Request) { r.From = "2026-1-1" }, wantErr: true},
		{name: "malformed asOf", mutate: func(r *Request) { r.AsOf = "2026-02-30" }, wantErr: true},
		{name: "empty manifest", mutate: func(r *Request) { r.ManifestSHA = " " }, wantErr: true},
		{name: "opaque manifest accepted", mutate: func(r *Request) { r.ManifestSHA = "sha256:synthetic" }},
		{name: "empty actor", mutate: func(r *Request) { r.Actor = " " }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := baseRequest()
			tt.mutate(&request)
			err := request.Validate()
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidRequest) {
					t.Fatalf("Validate() error = %v, want ErrInvalidRequest", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() unexpected error: %v", err)
			}
		})
	}
}

func TestPlan366And32DayBoundaries(t *testing.T) {
	request := baseRequest()
	request.From, request.Through = "2025-01-01", "2026-01-01"
	plan, err := Plan(request, Facts{})
	if err != nil {
		t.Fatalf("Plan(366) error: %v", err)
	}
	if plan.OutputDays != 366 {
		t.Fatalf("output days = %d, want 366", plan.OutputDays)
	}
	if len(plan.Blocks) != 12 {
		t.Fatalf("366-day blocks = %d, want 12", len(plan.Blocks))
	}
	for _, block := range plan.Blocks {
		if days := testSpanDays(t, block.From, block.Through); days < 1 || days > MaxBlockDays {
			t.Fatalf("block %d span = %d days, want 1..31", block.Index, days)
		}
	}
	if plan.Blocks[0].From != "2025-01-01" || plan.Blocks[11].Through != "2026-01-01" {
		t.Fatalf("block endpoints = %s..%s, want 2025-01-01..2026-01-01", plan.Blocks[0].From, plan.Blocks[11].Through)
	}

	request.From, request.Through = "2026-01-01", "2026-02-01"
	plan, err = Plan(request, Facts{})
	if err != nil {
		t.Fatalf("Plan(32) error: %v", err)
	}
	if plan.OutputDays != 32 || len(plan.Blocks) != 2 {
		t.Fatalf("32-day plan = %d days/%d blocks, want 32/2", plan.OutputDays, len(plan.Blocks))
	}
	if plan.Blocks[0].Through != "2026-01-31" {
		t.Fatalf("first block through = %s, want 2026-01-31", plan.Blocks[0].Through)
	}
	if plan.Blocks[1].From != "2026-02-01" || plan.Blocks[1].Through != "2026-02-01" {
		t.Fatalf("second block = %s..%s, want 2026-02-01..2026-02-01", plan.Blocks[1].From, plan.Blocks[1].Through)
	}

	request.From, request.Through = "2026-01-02", "2026-01-01"
	if _, err := Plan(request, Facts{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("zero-day Plan error = %v, want ErrInvalidRequest", err)
	}
}

func TestPlanWarmupAndStableInputHash(t *testing.T) {
	request := baseRequest()
	request.From, request.Through = "2026-03-01", "2026-05-29"
	request.AsOf = "2026-06-01"

	first, err := Plan(request, Facts{})
	if err != nil {
		t.Fatalf("Plan() error: %v", err)
	}
	second, err := Plan(request, Facts{})
	if err != nil {
		t.Fatalf("Plan() second error: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("Plan is not deterministic for identical input")
	}
	if first.OutputDays != 90 || len(first.Blocks) != 3 {
		t.Fatalf("plan = %d days/%d blocks, want 90/3", first.OutputDays, len(first.Blocks))
	}
	if first.Blocks[0].From != "2026-03-01" || first.Blocks[2].Through != "2026-05-29" {
		t.Fatalf("unexpected block endpoints: %+v", first.Blocks)
	}
	hashes := map[string]bool{}
	for _, block := range first.Blocks {
		if len(block.InputHash) != 64 {
			t.Fatalf("block %d hash length = %d", block.Index, len(block.InputHash))
		}
		if block.TargetAsOf != request.AsOf {
			t.Fatalf("block %d targetAsOf = %s, want %s", block.Index, block.TargetAsOf, request.AsOf)
		}
		if block.Status != BlockPending {
			t.Fatalf("block %d status = %s, want pending", block.Index, block.Status)
		}
		if hashes[block.InputHash] {
			t.Fatalf("duplicate block hash at %d", block.Index)
		}
		hashes[block.InputHash] = true
	}

	wantDays := map[string]int{
		ModelLoad:      LoadWarmupDays,
		ModelAbility:   AbilityWarmupDays,
		ModelVO2:       VO2WarmupDays,
		ModelRHR:       RHRWarmupDays,
		ModelReadiness: ReadinessWarmupDays,
	}
	if len(first.Warmup) != len(wantDays) {
		t.Fatalf("warmup entries = %d, want %d", len(first.Warmup), len(wantDays))
	}
	for _, warmup := range first.Warmup {
		days, ok := wantDays[warmup.Model]
		if !ok || warmup.Days != days {
			t.Fatalf("warmup %+v, want model/day %s/%d", warmup, warmup.Model, days)
		}
		if warmup.From != testAdd(t, request.From, -days) || warmup.Through != testAdd(t, request.From, -1) {
			t.Fatalf("warmup %s = %s..%s, want %s..%s", warmup.Model, warmup.From, warmup.Through, testAdd(t, request.From, -days), testAdd(t, request.From, -1))
		}
	}

	changed := request
	changed.DatasetID = "ds_2"
	other, err := Plan(changed, Facts{})
	if err != nil {
		t.Fatalf("Plan(changed) error: %v", err)
	}
	if other.Blocks[0].InputHash == first.Blocks[0].InputHash {
		t.Fatal("dataset change did not change block input hash")
	}
	changed = request
	changed.BlockDays = 30
	other, err = Plan(changed, Facts{})
	if err != nil {
		t.Fatalf("Plan(block size) error: %v", err)
	}
	if other.Blocks[0].InputHash == first.Blocks[0].InputHash {
		t.Fatal("block-size change did not change first block input hash")
	}

	for _, mutation := range []struct {
		name   string
		mutate func(*Request)
	}{
		{name: "manifest", mutate: func(r *Request) { r.ManifestSHA = strings.Repeat("b", 64) }},
		{name: "stack", mutate: func(r *Request) { r.StackID = "stack_2" }},
		{name: "through", mutate: func(r *Request) { r.Through = "2026-05-30" }},
		{name: "asOf", mutate: func(r *Request) { r.AsOf = "2026-06-02" }},
		{name: "view", mutate: func(r *Request) { r.View = ViewAsKnown }},
		{name: "operation", mutate: func(r *Request) { r.Operation = OperationDryRun }},
	} {
		changed = request
		mutation.mutate(&changed)
		other, err = Plan(changed, Facts{})
		if err != nil {
			t.Fatalf("Plan(%s) error: %v", mutation.name, err)
		}
		if other.Blocks[0].InputHash == first.Blocks[0].InputHash {
			t.Fatalf("%s change did not change first block input hash", mutation.name)
		}
	}
}

func TestPlanRejectsCapacityMissingInputsAndFuture(t *testing.T) {
	request := baseRequest()
	request.From, request.Through = "2026-03-01", "2026-05-29"
	request.AsOf = "2026-06-01"

	if _, err := Plan(request, Facts{MaxBlocks: 2}); !errors.Is(err, ErrCapacity) {
		t.Fatalf("capacity error = %v, want ErrCapacity", err)
	}
	if _, err := Plan(request, Facts{MissingInputs: []string{"workout_features@v2"}}); !errors.Is(err, ErrMissingInputs) {
		t.Fatalf("missing input error = %v, want ErrMissingInputs", err)
	}
	if _, err := Plan(request, Facts{Today: "2026-05-01"}); !errors.Is(err, ErrFuture) {
		t.Fatalf("future through error = %v, want ErrFuture", err)
	}
	asOfFuture := request
	asOfFuture.Through = "2026-04-01"
	if _, err := Plan(asOfFuture, Facts{Today: "2026-05-01"}); !errors.Is(err, ErrFuture) {
		t.Fatalf("future asOf error = %v, want ErrFuture", err)
	}
	if _, err := Plan(request, Facts{MaxBlocks: -1}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("negative capacity error = %v, want ErrInvalidRequest", err)
	}
	if _, err := Plan(request, Facts{Today: "2026-5-1"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("bad today error = %v, want ErrInvalidRequest", err)
	}
}

func completeBlocks(plan PlanResult) []Block {
	completed := make([]Block, len(plan.Blocks))
	copy(completed, plan.Blocks)
	for i := range completed {
		completed[i].Status = BlockComplete
	}
	return completed
}

func TestResumeKeepsIdentityAndRejectsConflict(t *testing.T) {
	request := baseRequest()
	request.From, request.Through = "2026-03-01", "2026-05-29"
	request.AsOf = "2026-06-01"
	plan, err := Plan(request, Facts{})
	if err != nil {
		t.Fatalf("Plan() error: %v", err)
	}
	completed := plan.Blocks[:1]
	remaining, conflict := Resume(request, completed)
	if conflict {
		t.Fatal("unexpected resume conflict")
	}
	if !reflect.DeepEqual(remaining, plan.Blocks[1:]) {
		t.Fatalf("remaining blocks drifted: %+v", remaining)
	}
	if len(remaining) != 2 || remaining[0].InputHash != plan.Blocks[1].InputHash {
		t.Fatalf("remaining identity not preserved: %+v", remaining)
	}

	remaining, conflict = Resume(request, completeBlocks(plan))
	if conflict || len(remaining) != 0 {
		t.Fatalf("full completion resume = %v/%d, want false/0", conflict, len(remaining))
	}

	tampered := append([]Block(nil), plan.Blocks[:1]...)
	tampered[0].InputHash = strings.Repeat("b", 64)
	if _, conflict := Resume(request, tampered); !conflict {
		t.Fatal("tampered input hash did not conflict")
	}

	changed := request
	changed.From = "2026-03-02"
	if _, conflict := Resume(changed, plan.Blocks[:1]); !conflict {
		t.Fatal("changed request did not conflict")
	}
	changed = request
	changed.Through = "2026-05-30"
	if _, conflict := Resume(changed, plan.Blocks[:1]); !conflict {
		t.Fatal("changed through did not conflict")
	}

	duplicate := []Block{plan.Blocks[0], plan.Blocks[0]}
	if _, conflict := Resume(request, duplicate); !conflict {
		t.Fatal("duplicate completion did not conflict")
	}
	outOfRange := []Block{{Index: 99, From: "2026-03-01", Through: "2026-03-31", InputHash: strings.Repeat("b", 64), TargetAsOf: request.AsOf}}
	if _, conflict := Resume(request, outOfRange); !conflict {
		t.Fatal("out-of-range completion did not conflict")
	}
	invalid := request
	invalid.BlockDays = 0
	if _, conflict := Resume(invalid, nil); !conflict {
		t.Fatal("invalid request did not conflict")
	}
}

func TestStatusAndCancel(t *testing.T) {
	blocks := []Block{
		{Index: 0, Status: BlockComplete},
		{Index: 1, Status: BlockRunning},
		{Index: 2, Status: BlockPending},
		{Index: 3, Status: BlockFailed},
	}
	status := Summarize(blocks)
	if status.Total != 4 || status.Complete != 1 || status.Running != 1 || status.Pending != 1 || status.Failed != 1 || status.Ready {
		t.Fatalf("status = %+v", status)
	}
	cancelled := Cancel(blocks)
	if cancelled[0].Status != BlockComplete {
		t.Fatal("cancel changed a complete block")
	}
	for _, block := range cancelled[1:] {
		if block.Status != BlockCancelled {
			t.Fatalf("cancel left block %d as %s", block.Index, block.Status)
		}
	}
	if blocks[1].Status != BlockRunning {
		t.Fatal("Cancel mutated its input")
	}
	ready := Summarize([]Block{{Status: BlockComplete}, {Status: BlockComplete}})
	if !ready.Ready {
		t.Fatalf("complete status not ready: %+v", ready)
	}
	if Summarize(nil).Ready {
		t.Fatal("empty status must not be ready")
	}
}

func rangesOf(t *testing.T, change FactChange) []Range {
	t.Helper()
	return AffectedRanges(change)
}

func TestAffectedRangesExactEndpoints(t *testing.T) {
	day := "2026-01-01"

	load := rangesOf(t, FactChange{Kind: FactWorkoutLoad, Day: day})
	wantLoad := []Range{
		{Model: ModelLoad, From: "2026-01-01", Through: "2026-06-29", Reason: "load_180d_tail"},
		{Model: ModelReadiness, From: "2026-01-01", Through: "2026-07-27", Reason: "workout_load_readiness_closure"},
		{Model: ModelTrend, From: "2026-01-01", Through: "2026-07-06", Reason: "trend_7d_tail"},
	}
	if !reflect.DeepEqual(load, wantLoad) {
		t.Fatalf("workout_load ranges = %+v, want %+v", load, wantLoad)
	}

	rhr := rangesOf(t, FactChange{Kind: FactRHR, Day: day})
	wantRHR := []Range{
		{Model: ModelLoad, From: "2026-01-02", Through: "2026-07-27", Reason: "rhr_load_closure_180d"},
		{Model: ModelReadiness, From: "2026-01-01", Through: "2026-07-27", Reason: "rhr_readiness_load_closure"},
		{Model: ModelRecovery, From: "2026-01-01", Through: "2026-01-29", Reason: "rhr_recovery_baseline_28d"},
		{Model: ModelReference, From: "2026-01-02", Through: "2026-01-29", Reason: "rhr_reference_28d"},
	}
	if !reflect.DeepEqual(rhr, wantRHR) {
		t.Fatalf("rhr ranges = %+v, want %+v", rhr, wantRHR)
	}

	vo2 := rangesOf(t, FactChange{Kind: FactVO2, Day: day})
	wantVO2 := []Range{
		{Model: ModelVO2, From: "2026-01-01", Through: "2026-02-17", Reason: "vo2_pool_42d_smoothing_6d"},
		{Model: ModelVO2, From: "2026-01-01", Through: "2026-03-16", Reason: "vo2_held_reference_27d"},
	}
	if !reflect.DeepEqual(vo2, wantVO2) {
		t.Fatalf("vo2 ranges = %+v, want %+v", vo2, wantVO2)
	}

	ability := rangesOf(t, FactChange{Kind: FactAbility, Day: day})
	wantAbility := []Range{
		{Model: ModelAbility, From: "2026-01-01", Through: "2026-04-13", Reason: "ability_pool_90d_smoothing_13d"},
		{Model: ModelAbility, From: "2026-01-01", Through: "2026-05-10", Reason: "ability_held_reference_27d"},
	}
	if !reflect.DeepEqual(ability, wantAbility) {
		t.Fatalf("ability ranges = %+v, want %+v", ability, wantAbility)
	}

	sleep := rangesOf(t, FactChange{Kind: FactSleep, Day: day})
	wantSleep := []Range{
		{Model: ModelReadiness, From: "2026-01-01", Through: "2026-01-31", Reason: "sleep_readiness_closure"},
		{Model: ModelRecovery, From: "2025-12-31", Through: "2026-01-03", Reason: "sleep_signal_neighborhood"},
		{Model: ModelRecovery, From: "2026-01-01", Through: "2026-01-29", Reason: "sleep_baseline_28d"},
		{Model: ModelRecovery, From: "2026-01-30", Through: "2026-01-31", Reason: "sleep_persistent_tail_2d"},
	}
	if !reflect.DeepEqual(sleep, wantSleep) {
		t.Fatalf("sleep ranges = %+v, want %+v", sleep, wantSleep)
	}

	daily := rangesOf(t, FactChange{Kind: FactDaily, Day: day})
	wantDaily := []Range{
		{Model: ModelReadiness, From: "2026-01-01", Through: "2026-01-31", Reason: "daily_readiness_closure"},
		{Model: ModelRecovery, From: "2025-12-31", Through: "2026-01-03", Reason: "daily_signal_neighborhood"},
		{Model: ModelRecovery, From: "2026-01-01", Through: "2026-01-29", Reason: "daily_baseline_28d"},
		{Model: ModelRecovery, From: "2026-01-30", Through: "2026-01-31", Reason: "daily_persistent_tail_2d"},
	}
	if !reflect.DeepEqual(daily, wantDaily) {
		t.Fatalf("daily ranges = %+v, want %+v", daily, wantDaily)
	}
}

func TestAffectedRangesOverlapSourceAnnotation(t *testing.T) {
	day := "2026-01-01"
	overlap := rangesOf(t, FactChange{Kind: FactOverlap, Day: day})
	if len(overlap) != 8 {
		t.Fatalf("overlap ranges = %d, want 8: %+v", len(overlap), overlap)
	}
	checkRange(t, overlap, ModelFeatures, "overlap_neighborhood", "2025-12-31", "2026-01-02")
	checkRange(t, overlap, ModelLoad, "overlap_load_closure", "2026-01-01", "2026-06-29")
	checkRange(t, overlap, ModelVO2, "overlap_vo2_closure", "2026-01-01", "2026-02-17")
	checkRange(t, overlap, ModelVO2, "overlap_vo2_held_reference", "2026-01-01", "2026-03-16")
	checkRange(t, overlap, ModelAbility, "overlap_ability_closure", "2026-01-01", "2026-04-13")
	checkRange(t, overlap, ModelAbility, "overlap_ability_held_reference", "2026-01-01", "2026-05-10")
	checkRange(t, overlap, ModelReadiness, "overlap_readiness_closure", "2026-01-01", "2026-07-27")
	checkRange(t, overlap, ModelTrend, "overlap_trend_closure", "2026-01-01", "2026-07-06")

	source := rangesOf(t, FactChange{Kind: FactSource, Day: day})
	if len(source) != 9 {
		t.Fatalf("source ranges = %d, want 9: %+v", len(source), source)
	}
	checkRange(t, source, ModelAbility, "source_ability_closure", "2026-01-01", "2026-04-13")
	checkRange(t, source, ModelAbility, "source_ability_held_reference", "2026-01-01", "2026-05-10")
	checkRange(t, source, ModelVO2, "source_vo2_closure", "2026-01-01", "2026-02-17")
	checkRange(t, source, ModelVO2, "source_vo2_held_reference", "2026-01-01", "2026-03-16")
	checkRange(t, source, ModelRecovery, "source_recovery_closure", "2025-12-31", "2026-01-31")
	checkRange(t, source, ModelReadiness, "source_readiness_closure", "2025-12-31", "2026-07-27")

	annotation := rangesOf(t, FactChange{Kind: FactAnnotation, Day: day})
	if len(annotation) != 2 {
		t.Fatalf("annotation ranges = %d, want 2: %+v", len(annotation), annotation)
	}
	checkRange(t, annotation, ModelAbility, "annotation_ability_closure", "2026-01-01", "2026-04-13")
	checkRange(t, annotation, ModelAbility, "annotation_ability_held_reference", "2026-01-01", "2026-05-10")

	if got := AffectedRanges(FactChange{Kind: "bogus", Day: day}); got != nil {
		t.Fatalf("invalid kind ranges = %+v, want nil", got)
	}
	if got := AffectedRanges(FactChange{Kind: FactWorkoutLoad, Day: "2026-02-30"}); got != nil {
		t.Fatalf("invalid date ranges = %+v, want nil", got)
	}
	if got := AffectedRanges(FactChange{Kind: FactWorkoutLoad, From: "2026-01-02", Through: "2026-01-01"}); got != nil {
		t.Fatalf("inverted range = %+v, want nil", got)
	}
}

func TestAffectedRangesMultiDaySpan(t *testing.T) {
	ranges := AffectedRanges(FactChange{Kind: FactWorkoutLoad, From: "2026-01-10", Through: "2026-01-12"})
	want := []Range{
		{Model: ModelLoad, From: "2026-01-10", Through: testAdd(t, "2026-01-12", 179), Reason: "load_180d_tail"},
		{Model: ModelReadiness, From: "2026-01-10", Through: testAdd(t, "2026-01-12", 207), Reason: "workout_load_readiness_closure"},
		{Model: ModelTrend, From: "2026-01-10", Through: testAdd(t, "2026-01-12", 186), Reason: "trend_7d_tail"},
	}
	if !reflect.DeepEqual(ranges, want) {
		t.Fatalf("multi-day ranges = %+v, want %+v", ranges, want)
	}
}

func checkRange(t *testing.T, ranges []Range, model, reason, from, through string) {
	t.Helper()
	for _, current := range ranges {
		if current.Model == model && current.Reason == reason {
			if current.From != from || current.Through != through {
				t.Fatalf("%s/%s = %s..%s, want %s..%s", model, reason, current.From, current.Through, from, through)
			}
			return
		}
	}
	t.Fatalf("range %s/%s not found in %+v", model, reason, ranges)
}

func floatPtr(value float64) *float64 { return &value }

func hasChange(changes []Change, kind DiffKind, model, reason string) (Change, bool) {
	for _, change := range changes {
		if change.Kind == kind && change.Model == model && change.Reason == reason {
			return change, true
		}
	}
	return Change{}, false
}

func TestDiffRedactedDefault(t *testing.T) {
	before := Snapshot{
		View: "revised",
		AsOf: "2026-02-01",
		Observations: []Observation{
			{Model: "load", Day: "2026-01-01", State: "available", Value: floatPtr(50), Unit: "RAI_AU", Version: "v1", Mode: "revised", Reasons: []string{"r1"}},
			{Model: "readiness", Day: "2026-01-01", State: "available", Value: nil, Unit: "score", Version: "v1", Mode: "revised", Reasons: []string{"baseline"}},
		},
	}
	after := Snapshot{
		View: "as_known",
		AsOf: "2026-02-02",
		Observations: []Observation{
			{Model: "load", Day: "2026-01-01", State: "limited", Value: floatPtr(75), Unit: "RAI_AU", Version: "v2", Mode: "as_known", Reasons: []string{"r2", "r1"}},
			{Model: "readiness", Day: "2026-01-01", State: "available", Value: floatPtr(80), Unit: "score", Version: "v1", Mode: "revised", Reasons: []string{"baseline"}},
		},
	}
	changes := Diff(before, after)
	if _, ok := hasChange(changes, DiffBoundary, "*", "view"); !ok {
		t.Fatalf("missing view boundary: %+v", changes)
	}
	if boundary, ok := hasChange(changes, DiffBoundary, "load", "version"); !ok || boundary.From != "v1" || boundary.To != "v2" {
		t.Fatalf("missing version boundary: %+v", changes)
	}
	if boundary, ok := hasChange(changes, DiffBoundary, "load", "mode"); !ok || boundary.From != "revised" || boundary.To != "as_known" {
		t.Fatalf("missing mode boundary: %+v", changes)
	}
	if state, ok := hasChange(changes, DiffState, "load", "state"); !ok || state.From != "available" || state.To != "limited" {
		t.Fatalf("missing state change: %+v", changes)
	}
	if reason, ok := hasChange(changes, DiffReason, "load", "reasons"); !ok || reason.From != "r1" || reason.To != "r1,r2" {
		t.Fatalf("missing reason change: %+v", changes)
	}
	if _, ok := hasChange(changes, DiffValueBucket, "load", "value_bucket"); ok {
		t.Fatalf("numeric comparison crossed a model boundary: %+v", changes)
	}
	if null, ok := hasChange(changes, DiffNull, "readiness", "null_to_value"); !ok || null.From != "null" || null.To != "value" {
		t.Fatalf("missing null transition: %+v", changes)
	}
	for _, change := range changes {
		if change.Kind == DiffValue {
			t.Fatalf("redacted diff leaked exact value change: %+v", change)
		}
		for _, token := range []string{"50", "75", "80"} {
			if change.From == token || change.To == token {
				t.Fatalf("redacted diff leaked value %q: %+v", token, change)
			}
		}
	}

	after.View = before.View
	after.Observations[0].Version = before.Observations[0].Version
	after.Observations[0].Mode = before.Observations[0].Mode
	comparable := Diff(before, after)
	if bucket, ok := hasChange(comparable, DiffValueBucket, "load", "value_bucket"); !ok || bucket.From != "bin_5" || bucket.To != "bin_7" {
		t.Fatalf("missing comparable value bucket: %+v", comparable)
	}
	exact := DiffWithOptions(before, after, DiffOptions{ExposeValues: true})
	value, ok := hasChange(exact, DiffValue, "load", "value")
	if !ok || value.From != "50" || value.To != "75" {
		t.Fatalf("exact value diff = %+v", exact)
	}
}

func TestDiffBoundariesAndNullPresence(t *testing.T) {
	before := Snapshot{View: "revised", Observations: []Observation{
		{Model: "load", Day: "2026-01-01", State: "available", Unit: "RAI_AU", Version: "v1", Mode: "revised"},
		{Model: "removed", Day: "2026-01-01", State: "available", Value: floatPtr(12)},
		{Model: "readiness", Day: "2026-01-01", State: "available", Value: floatPtr(70)},
	}}
	after := Snapshot{View: "revised", Observations: []Observation{
		{Model: "load", Day: "2026-01-01", State: "available", Unit: "bpm", Version: "v2", Mode: "as_known"},
		{Model: "added", Day: "2026-01-01", State: "collecting", Value: floatPtr(3)},
		{Model: "readiness", Day: "2026-01-01", State: "available"},
	}}
	changes := Diff(before, after)
	if boundary, ok := hasChange(changes, DiffBoundary, "load", "unit"); !ok || boundary.From != "RAI_AU" || boundary.To != "bpm" {
		t.Fatalf("missing unit boundary: %+v", changes)
	}
	if _, ok := hasChange(changes, DiffBoundary, "load", "version"); !ok {
		t.Fatalf("missing version boundary: %+v", changes)
	}
	if _, ok := hasChange(changes, DiffBoundary, "load", "mode"); !ok {
		t.Fatalf("missing mode boundary: %+v", changes)
	}
	if null, ok := hasChange(changes, DiffNull, "removed", "observation_removed"); !ok || null.From != "value" || null.To != "null" {
		t.Fatalf("missing removed null transition: %+v", changes)
	}
	if null, ok := hasChange(changes, DiffNull, "added", "observation_added"); !ok || null.From != "null" || null.To != "value" {
		t.Fatalf("missing added null transition: %+v", changes)
	}
	if null, ok := hasChange(changes, DiffNull, "readiness", "value_to_null"); !ok || null.From != "value" || null.To != "null" {
		t.Fatalf("missing value-to-null transition: %+v", changes)
	}
}

func TestDiffReasonOrderIsNotAChange(t *testing.T) {
	before := Snapshot{Observations: []Observation{{Model: "recovery", Day: "2026-01-01", State: "available", Reasons: []string{"b", "a"}}}}
	after := Snapshot{Observations: []Observation{{Model: "recovery", Day: "2026-01-01", State: "available", Reasons: []string{"a", "b"}}}}
	if changes := Diff(before, after); len(changes) != 0 {
		t.Fatalf("reason order produced changes: %+v", changes)
	}
}
