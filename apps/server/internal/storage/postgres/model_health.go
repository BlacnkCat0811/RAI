package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/analysis"
	"rai/server/internal/modelmanifest"
	"rai/server/internal/syncing"
)

// HealthCounts is a count per status, tier or reason code. It never carries a
// health value, an identifier or a timestamp.
type HealthCounts map[string]int

func (c HealthCounts) add(key string) {
	if key == "" {
		key = "none"
	}
	c[key]++
}

// PublishedHealth summarizes one product artifact over the published days.
type PublishedHealth struct {
	PublishedDays int          `json:"publishedDays"`
	MissingDays   int          `json:"missingDays"`
	Status        HealthCounts `json:"status"`
	Quality       HealthCounts `json:"quality"`
	Reasons       HealthCounts `json:"reasonCodes"`
}

// WorkoutHealth is a dry run of the current code's per-Workout stages over the
// window. Nothing is written.
type WorkoutHealth struct {
	Workouts           int          `json:"workouts"`
	InputFailures      HealthCounts `json:"inputFailures"`
	LoadTier           HealthCounts `json:"loadTier"`
	LoadStatus         HealthCounts `json:"loadStatus"`
	LoadReasons        HealthCounts `json:"loadReasonCodes"`
	HRCoverage         HealthCounts `json:"hrCoverage"`
	HRMaxGap           HealthCounts `json:"hrMaxGapSeconds"`
	Performance        HealthCounts `json:"performanceQualification"`
	PerformanceReasons HealthCounts `json:"performanceReasonCodes"`
	VO2                HealthCounts `json:"vo2Qualification"`
	VO2Reasons         HealthCounts `json:"vo2ReasonCodes"`
	StableSourceSets   int          `json:"distinctStableSourceSets"`
	OverlapUnresolved  int          `json:"overlapUnresolved"`
}

// DailyHealthFacts counts daily health facts and how many source epochs they
// form with and without software versions in the identity.
type DailyHealthFacts struct {
	Facts                HealthCounts `json:"facts"`
	FullEpochs           HealthCounts `json:"distinctFullEpochs"`
	StableEpochs         HealthCounts `json:"distinctStableEpochs"`
	HRVDistinctDurations int          `json:"hrvDistinctSampleDurations"`
}

type ModelStackHealth struct {
	Channel     string                     `json:"channel"`
	StackIsCode bool                       `json:"channelStackIsCurrentCode"`
	Days        int                        `json:"days"`
	Published   map[string]PublishedHealth `json:"published"`
	Workouts    WorkoutHealth              `json:"workoutsDryRun"`
	DailyHealth DailyHealthFacts           `json:"dailyHealthDryRun"`
}

var healthArtifacts = []string{"load_state", "physiological_recovery", "training_readiness", "performance_state_v2", "vo2max_submax", "vo2max_external_projection"}

func coverageBucket(v float64) string {
	switch {
	case v >= .98:
		return ">=0.98"
	case v >= .95:
		return "0.95-0.98"
	case v >= .80:
		return "0.80-0.95"
	default:
		return "<0.80"
	}
}

func gapBucket(v float64) string {
	switch {
	case v <= 15:
		return "<=15"
	case v <= 30:
		return "15-30"
	case v <= 180:
		return "30-180"
	default:
		return ">180"
	}
}

// ModelStackHealth reports, as counts only, how the channel's published day
// releases fared over the last days, and dry-runs the current code's
// per-Workout and daily-health stages over the same window. It is read-only
// and is the Phase 0 baseline for model availability.
func (s *Store) ModelStackHealth(ctx context.Context, dataset, channel string, days int, now time.Time) (ModelStackHealth, error) {
	out := ModelStackHealth{Channel: channel, Days: days, Published: map[string]PublishedHealth{}}
	if !syncing.ValidID(dataset) || days < 1 || days > 186 || (channel != "active" && channel != "candidate") {
		return out, analysis.Invalid()
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	var stack, epoch, collector, timezone string
	var revision int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(m.stack_id,''),ds.epoch::text,COALESCE(c.collector_epoch::text,''),ds.timezone,ds.commit_rev
 FROM datasets ds LEFT JOIN analysis_model_channels m ON m.dataset_id=ds.id AND m.channel=$2
 LEFT JOIN collector_leases c ON c.dataset_id=ds.id WHERE ds.id=$1 AND ds.retired_at IS NULL`, dataset, channel).Scan(&stack, &epoch, &collector, &timezone, &revision); err != nil {
		return out, err
	}
	if stack == "" {
		return out, errors.New("model_channel_missing")
	}
	out.StackIsCode = stack == modelmanifest.StackID()
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return out, analysis.Invalid()
	}
	local := now.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	first := today.AddDate(0, 0, -(days - 1))
	if err = publishedHealth(ctx, tx, &out, dataset, stack, first, today); err != nil {
		return out, err
	}
	if err = workoutHealth(ctx, tx, &out, dataset, first, now, revision, loc); err != nil {
		return out, err
	}
	l := Lease{DatasetID: dataset, TargetGeneration: 1, ID: "model-stack-health"}
	in := analysis.Snapshot{Generation: 1, Revision: revision}
	if err = loadRecoveryFacts(ctx, tx, l, &in, now, now, today, epoch, collector, l.ID); err != nil {
		return out, err
	}
	out.DailyHealth = DailyHealthFacts{Facts: HealthCounts{}, FullEpochs: HealthCounts{}, StableEpochs: HealthCounts{}}
	full, stable, durations := map[string]map[string]bool{}, map[string]map[string]bool{}, map[int64]bool{}
	for _, f := range in.RecoveryInput.Facts {
		kind := map[string]string{"HKQuantityTypeIdentifierHeartRateVariabilitySDNN": "hrv", "HKQuantityTypeIdentifierRestingHeartRate": "rhr", "HKCategoryTypeIdentifierSleepAnalysis": "sleep"}[f.Type]
		out.DailyHealth.Facts.add(kind)
		if full[kind] == nil {
			full[kind], stable[kind] = map[string]bool{}, map[string]bool{}
		}
		full[kind][f.Epoch], stable[kind][f.StableEpoch] = true, true
		if kind == "hrv" {
			durations[(f.End-f.Start)/1_000_000] = true
		}
	}
	for kind := range full {
		out.DailyHealth.FullEpochs[kind] = len(full[kind])
		out.DailyHealth.StableEpochs[kind] = len(stable[kind])
	}
	out.DailyHealth.HRVDistinctDurations = len(durations)
	return out, nil
}

func publishedHealth(ctx context.Context, tx pgx.Tx, out *ModelStackHealth, dataset, stack string, first, today time.Time) error {
	rows, err := tx.Query(ctx, `SELECT p.day::text,a.artifact_key,COALESCE(a.payload->>'status',''),
 COALESCE(a.payload->>'quality',a.payload->'days'->0->>'quality',''),COALESCE(a.payload->'reasonCodes','[]'::jsonb),
 COALESCE(jsonb_typeof(a.payload->'days'->0->'acute'->'value')='number',false),
 COALESCE(jsonb_typeof(a.payload->'days'->0->'chronic'->'value')='number',false)
 FROM analysis_publications p
 JOIN analysis_artifacts a ON a.dataset_id=p.dataset_id AND a.run_id=p.published_run_id AND a.artifact_key=ANY($3)
 WHERE p.dataset_id=$1 AND p.scope_key='model/'||$2::text||'/athlete/day/v2/'||p.day::text
 AND p.metric_family='model_product_v2/'||a.artifact_key AND p.day BETWEEN $4::date AND $5::date`,
		dataset, stack, healthArtifacts, first.Format(time.DateOnly), today.Format(time.DateOnly))
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[string]map[string]bool{}
	for rows.Next() {
		var day, key, status, quality string
		var reasons []byte
		var acute, chronic bool
		if err = rows.Scan(&day, &key, &status, &quality, &reasons, &acute, &chronic); err != nil {
			return err
		}
		h, ok := out.Published[key]
		if !ok {
			h = PublishedHealth{Status: HealthCounts{}, Quality: HealthCounts{}, Reasons: HealthCounts{}}
		}
		if seen[key] == nil {
			seen[key] = map[string]bool{}
		}
		if seen[key][day] {
			continue
		}
		seen[key][day] = true
		h.PublishedDays++
		if key == "load_state" {
			status = map[bool]string{true: "acute_and_chronic", false: "unavailable"}[acute && chronic]
			if acute != chronic {
				status = map[bool]string{true: "acute_only", false: "chronic_only"}[acute]
			}
		}
		h.Status.add(status)
		h.Quality.add(quality)
		var codes []string
		if json.Unmarshal(reasons, &codes) == nil {
			for _, c := range codes {
				h.Reasons.add(c)
			}
		}
		out.Published[key] = h
	}
	if err = rows.Err(); err != nil {
		return err
	}
	total := int(today.Sub(first).Hours()/24+.5) + 1
	for _, key := range healthArtifacts {
		h, ok := out.Published[key]
		if !ok {
			h = PublishedHealth{Status: HealthCounts{}, Quality: HealthCounts{}, Reasons: HealthCounts{}}
		}
		h.MissingDays = total - h.PublishedDays
		out.Published[key] = h
	}
	return nil
}

func workoutHealth(ctx context.Context, tx pgx.Tx, out *ModelStackHealth, dataset string, first, now time.Time, revision int64, loc *time.Location) error {
	w := WorkoutHealth{InputFailures: HealthCounts{}, LoadTier: HealthCounts{}, LoadStatus: HealthCounts{}, LoadReasons: HealthCounts{},
		HRCoverage: HealthCounts{}, HRMaxGap: HealthCounts{}, Performance: HealthCounts{}, PerformanceReasons: HealthCounts{}, VO2: HealthCounts{}, VO2Reasons: HealthCounts{}}
	l := Lease{DatasetID: dataset, TargetGeneration: 1, ID: "model-stack-health"}
	stack := modelmanifest.StackID()
	workouts, err := stackWorkouts(ctx, tx, l, stack, first, now, now, revision)
	if err != nil {
		return err
	}
	inputs, err := stackInputs(ctx, tx, dataset, now)
	if err != nil {
		return err
	}
	_, _, resolutions, _ := stackAnnotations(inputs, now)
	_, ambiguous := stackOverlap(workouts, resolutions, now)
	restingHR, err := stackRestingHRFacts(ctx, tx, dataset, first.AddDate(0, 0, -30), now, now)
	if err != nil {
		return err
	}
	sets := map[string]bool{}
	for _, x := range workouts {
		w.Workouts++
		if x.failure != "" {
			w.InputFailures.add(x.failure)
		}
		if ambiguous[x.id] {
			w.OverlapUnresolved++
		}
		r := stackWorkoutLoad(x, inputs, now, restingHR, loc, stack, 1, ambiguous[x.id])
		w.LoadTier.add(r.load.QualityTier)
		w.LoadStatus.add(r.load.Status)
		for _, c := range r.load.ReasonCodes {
			w.LoadReasons.add(c)
		}
		if x.failure != "" {
			continue
		}
		f := x.feature
		w.HRCoverage.add(coverageBucket(f.HeartRate.ReconstructedCoverage))
		w.HRMaxGap.add(gapBucket(f.HeartRate.MaxGap))
		w.Performance.add(f.Qualification.Performance.Status)
		for _, c := range f.Qualification.Performance.Reasons {
			w.PerformanceReasons.add(string(c))
		}
		w.VO2.add(f.Qualification.VO2.Status)
		for _, c := range f.Qualification.VO2.Reasons {
			w.VO2Reasons.add(string(c))
		}
		keys := append([]string{}, f.Evidence.SourceEpochs...)
		sort.Strings(keys)
		b, _ := json.Marshal(keys)
		sets[string(b)] = true
	}
	w.StableSourceSets = len(sets)
	out.Workouts = w
	return nil
}
