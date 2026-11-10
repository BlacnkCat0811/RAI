package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	ve "rai/server/internal/vo2maxexternal"
	"time"

	"rai/server/internal/analysis"
	"rai/server/internal/modelmanifest"
	pf "rai/server/internal/performance"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
	tl "rai/server/internal/trainingload"
	wf "rai/server/internal/workoutfeatures"
)

type Queue interface {
	RequeueExpired(context.Context) (int, error)
	Claim(context.Context, []string) (analysis.Lease, error)
	Renew(context.Context, analysis.Lease) error
	LoadAnalysis(context.Context, analysis.Lease, time.Time) (analysis.Snapshot, error)
	StartAnalysisRun(context.Context, analysis.Lease, analysis.Snapshot) (string, error)
	PublishAnalysis(context.Context, analysis.Lease, string, analysis.Snapshot, []analysis.Metric) (string, error)
	FailAnalysis(context.Context, analysis.Lease, string, string, bool) error
	AdvanceAnalysisDay(context.Context, time.Time) error
}

var kinds = []string{"analysis.workout", "analysis.sample", "analysis.series", "analysis.training_state", "training.plan", wf.JobKind, tl.JobKind, tl.StateJobKind, pf.JobKind, ve.JobKind, rc.JobKind, rd.JobKind, modelmanifest.StackJobKind}

// Run uses one processing slot. The short poll waits on cancellation and the
// independent renewal loop cancels calculation immediately after lease loss.
//
// The caller passes one explicit as-of instant so day advancement and analysis
// snapshots stay deterministic for the whole run; the value is threaded
// unchanged into every downstream operation.
func Run(ctx context.Context, q Queue, log *slog.Logger, asOf time.Time) error {
	log.Info("worker_started", "handlers", len(kinds), "processing_slots", 1)
	go runWorkoutIngests(ctx, q, log)
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	recovery := time.NewTicker(15 * time.Second)
	defer recovery.Stop()
	elapsed := func() time.Duration { return time.Since(asOf) }
	recoverExpired := func() {
		n, e := q.RequeueExpired(ctx)
		if e != nil && ctx.Err() == nil {
			log.Warn("queue_recovery_failed", "reason", "database_unavailable")
		}
		if n > 0 {
			log.Info("leases_recovered", "count", n)
		}
		if e = q.AdvanceAnalysisDay(ctx, asOf); e != nil && ctx.Err() == nil {
			log.Warn("analysis_day_advance_failed", "reason", "database_unavailable")
		}
	}
	recoverExpired()
	for {
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-recovery.C:
			recoverExpired()
			continue
		case <-poll.C:
		}
		claimStart := elapsed()
		l, e := q.Claim(ctx, kinds)
		if e == nil {
			log.Info("job_claim", "component", "worker", "operation", "claim", "status", "claimed", "duration_ms", (elapsed() - claimStart).Milliseconds())
			Process(ctx, q, l, log, asOf)
			continue
		}
	}
}

// WorkoutIngestQueue is the independent UUID ingest slot. It shares the
// process with analysis but never waits for the analysis loop.
type WorkoutIngestQueue interface {
	ProcessNextWorkoutIngest(context.Context) (syncing.WorkoutIngestOutcome, error)
	RecoverExpiredWorkoutIngests(context.Context) (int, error)
}

func runWorkoutIngests(ctx context.Context, q Queue, log *slog.Logger) {
	ingest, ok := q.(WorkoutIngestQueue)
	if !ok {
		return
	}
	recoverExpired := func() {
		n, err := ingest.RecoverExpiredWorkoutIngests(ctx)
		if err != nil && ctx.Err() == nil {
			log.Warn("workout_ingest_recovery_failed", "reason", "database_unavailable")
		}
		if n > 0 {
			log.Info("workout_ingest_expired", "count", n)
		}
	}
	recoverExpired()
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	recovery := time.NewTicker(15 * time.Second)
	defer recovery.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-recovery.C:
			recoverExpired()
		default:
		}
		outcome, err := ingest.ProcessNextWorkoutIngest(ctx)
		if err != nil {
			if ctx.Err() == nil {
				log.Warn("workout_ingest_processing_failed", "reason", "database_unavailable")
			}
		} else if outcome.Claimed {
			log.Info("workout_ingest", "component", "worker", "operation", "workout_ingest",
				"workout_hash", logIdentity(outcome.WorkoutID), "status", outcome.Status,
				"error_code", outcome.ErrorCode, "attempts", outcome.Attempts)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		}
	}
}

// Process is also the deterministic single-claim integration/administrative
// harness. Algorithms remain in analysis; storage owns publication transactions.
//
// Production callers must pass one explicit as-of instant so the claimed job is
// evaluated against the same moment as day advancement.
func Process(ctx context.Context, q Queue, l analysis.Lease, log *slog.Logger, asOf time.Time) {
	processWithRenewal(ctx, q, l, log, 20*time.Second, asOf)
}
func processWithRenewal(ctx context.Context, q Queue, l analysis.Lease, log *slog.Logger, renewEvery time.Duration, at time.Time) {
	elapsed := func() time.Duration { return time.Since(at) }
	name, algorithmErr := analysis.Algorithm(l.Kind)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	renewalDone := make(chan struct{})
	go func() {
		defer close(renewalDone)
		t := time.NewTicker(renewEvery)
		defer t.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-t.C:
				if e := q.Renew(runCtx, l); e != nil {
					cancel()
					return
				}
			}
		}
	}()
	if l.Kind == modelmanifest.StackJobKind {
		status := "FAILED"
		var err error
		if processor, ok := q.(interface {
			ProcessModelStack(context.Context, analysis.Lease, time.Time) (string, error)
		}); ok {
			status, err = processor.ProcessModelStack(runCtx, l, at)
		} else {
			err = &analysis.Failure{Code: "analysis_unsupported_algorithm"}
		}
		cancel()
		<-renewalDone
		if err != nil && ctx.Err() == nil {
			status = "FAILED"
			code, retryable := analysis.ErrorCode(err)
			failCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			if failErr := q.FailAnalysis(failCtx, l, "", code, retryable); failErr != nil && ctx.Err() == nil {
				log.Warn("analysis_failure_record_failed", "reason", "database_unavailable")
			}
			stop()
		}
		log.Info("model_stack_execution", "component", "worker", "status", status, "duration_ms", elapsed().Milliseconds())
		return
	}
	if l.Kind == "training.plan" {
		status := "FAILED"
		var in training.Input
		id := ""
		var err error
		publicationDuration := time.Duration(0)
		tq, ok := q.(TrainingQueue)
		if ok {
			in, err = tq.LoadTraining(runCtx, l, at)
			if err == nil {
				id, err = tq.StartTrainingRun(runCtx, l, in)
			}
			var base training.Base
			if err == nil && in.Generation == l.TargetGeneration {
				base, err = training.Generate(in.Context)
			}
			if err == nil {
				publicationStart := elapsed()
				status, err = tq.PublishTraining(runCtx, l, id, in, base)
				publicationDuration = elapsed() - publicationStart
			}
		}
		cancel()
		<-renewalDone
		if err != nil {
			status = "FAILED"
		}
		if ok && err != nil && ctx.Err() == nil {
			failCtx, c := context.WithTimeout(ctx, 5*time.Second)
			if failErr := tq.FailTraining(failCtx, l, id, err); failErr != nil && ctx.Err() == nil {
				log.Warn("training_failure_record_failed", "reason", "database_unavailable")
			}
			c()
		}
		baseID, effectiveID := "", ""
		overrideCount := int64(0)
		if in.Plan.Effective != nil {
			baseID = in.Plan.Effective.BaseID
			effectiveID = in.Plan.Effective.ID
			overrideCount = in.Plan.OverrideRevision
		}
		if ok && status == "SUCCEEDED" && err == nil {
			logCtx, c := context.WithTimeout(ctx, 5*time.Second)
			b, e, n, readErr := tq.TrainingVersions(logCtx, l, id)
			c()
			if readErr == nil {
				baseID = b
				effectiveID = e
				overrideCount = n
			} else if ctx.Err() == nil {
				log.Warn("training_versions_read_failed", "reason", "database_unavailable")
			}
		}
		errorCode := ""
		if err != nil {
			errorCode = "training_failed"
		}
		log.Info("training_execution", "component", "worker", "operation", "training", "error_code", errorCode, "plan_hash", logIdentity(trainingPlanFromScope(l.ScopeKey)), "generation", l.TargetGeneration, "base_hash", logIdentity(baseID), "effective_hash", logIdentity(effectiveID), "job_hash", logIdentity(l.ID), "run_hash", logIdentity(id), "generator_version", training.Generator, "override_count", overrideCount, "duration_ms", elapsed().Milliseconds(), "publication_ms", publicationDuration.Milliseconds(), "status", status)
		return
	}
	if algorithmErr != nil {
		code, retryable := analysis.ErrorCode(algorithmErr)
		failCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		if failErr := q.FailAnalysis(failCtx, l, "", code, retryable); failErr != nil && ctx.Err() == nil {
			log.Warn("analysis_failure_record_failed", "reason", "database_unavailable")
		}
		stop()
		cancel()
		<-renewalDone
		log.Info("analysis_execution", "component", "worker", "operation", "analysis", "error_code", code, "job_hash", logIdentity(l.ID), "scope_hash", logIdentity(l.ScopeKey), "generation", l.TargetGeneration, "algorithm", "", "algorithm_version", analysis.BundleVersion(l.Kind), "run_hash", "", "duration_ms", elapsed().Milliseconds(), "status", "FAILED", "result_status", "FAILED")
		return
	}
	snapshotStart := elapsed()
	in, err := q.LoadAnalysis(runCtx, l, at)
	snapshotDuration := elapsed() - snapshotStart
	id := ""
	if err != nil && in.Generation > 0 {
		in.InputVersion += "/incomplete"
		var startErr error
		id, startErr = q.StartAnalysisRun(runCtx, l, in)
		if startErr != nil && ctx.Err() == nil {
			log.Warn("analysis_run_start_failed", "reason", "database_unavailable")
		}
	}
	status := "FAILED"
	if err == nil {
		id, err = q.StartAnalysisRun(runCtx, l, in)
	}
	var ms []analysis.Metric
	publicationDuration := time.Duration(0)
	algorithmDuration := time.Duration(0)
	if err == nil && in.Generation == l.TargetGeneration {
		algorithmStart := elapsed()
		if l.Kind == rd.JobKind {
			ms, err = analysis.CalculateCandidateReadiness(&in)
		} else if l.Kind == rc.JobKind {
			ms, err = analysis.CalculateCandidateRecovery(&in)
		} else if l.Kind == ve.JobKind {
			ms, err = analysis.CalculateCandidateVO2External(&in)
		} else if l.Kind == pf.JobKind {
			ms, err = analysis.CalculateCandidatePerformance(&in)
		} else if l.Kind == tl.JobKind || l.Kind == tl.StateJobKind {
			ms, err = analysis.CalculateCandidateLoad(&in, l.Kind)
		} else if l.Kind == wf.JobKind {
			if in.FeatureInput == nil {
				err = analysis.Invalid()
			} else {
				var f wf.WorkoutFeatures
				f, err = wf.ComputeNormalized(runCtx, *in.FeatureInput)
				if err != nil {
					err = &analysis.Failure{Code: mapFeatureError(err)}
				} else {
					in.FeatureArtifact = &f
					if in.ExposureRequired {
						var exposure wf.HRExposure
						exposure, err = wf.BuildHRExposure(*in.FeatureInput)
						if err != nil {
							err = &analysis.Failure{Code: mapFeatureError(err)}
						} else {
							in.ExposureArtifact = &exposure
						}
					}
					ms = featureMetrics(f)
				}
			}
		} else {
			ms, err = analysis.Analyze(runCtx, l.Kind, in)
		}
		algorithmDuration = elapsed() - algorithmStart
	}
	if err == nil {
		publicationStart := elapsed()
		status, err = q.PublishAnalysis(runCtx, l, id, in, ms)
		publicationDuration = elapsed() - publicationStart
	}
	cancel()
	<-renewalDone
	if err != nil && ctx.Err() == nil {
		status = "FAILED"
		code, retryable := analysis.ErrorCode(err)
		failCtx, c := context.WithTimeout(ctx, 5*time.Second)
		if failErr := q.FailAnalysis(failCtx, l, id, code, retryable); failErr != nil && ctx.Err() == nil {
			log.Warn("analysis_failure_record_failed", "reason", "database_unavailable")
		}
		c()
	}
	errorCode := ""
	if err != nil {
		errorCode, _ = analysis.ErrorCode(err)
	}
	log.Info("analysis_execution", "component", "worker", "operation", "analysis", "error_code", errorCode, "job_hash", logIdentity(l.ID), "scope_hash", logIdentity(l.ScopeKey), "generation", l.TargetGeneration, "algorithm", name, "algorithm_version", analysis.BundleVersion(l.Kind), "snapshot_ms", float64(snapshotDuration.Microseconds())/1000, "algorithm_ms", float64(algorithmDuration.Microseconds())/1000, "run_hash", logIdentity(id), "duration_ms", elapsed().Milliseconds(), "publication_ms", publicationDuration.Milliseconds(), "status", status, "result_status", status)
}

// TrainingQueue extends the same queue with a distinct domain handler.
type TrainingQueue interface {
	TrainingVersions(context.Context, analysis.Lease, string) (string, string, int64, error)
	LoadTraining(context.Context, analysis.Lease, time.Time) (training.Input, error)
	StartTrainingRun(context.Context, analysis.Lease, training.Input) (string, error)
	PublishTraining(context.Context, analysis.Lease, string, training.Input, training.Base) (string, error)
	FailTraining(context.Context, analysis.Lease, string, error) error
}

func trainingPlanFromScope(scope string) string {
	if len(scope) > 5 {
		return scope[5:]
	}
	return ""
}

func mapFeatureError(err error) string {
	if wf.ErrorCode(err) == "input_limit" || wf.ErrorCode(err) == "feature_artifact_limit" {
		return "analysis_input_limit"
	}
	return "analysis_invalid_input"
}
func featureMetrics(f wf.WorkoutFeatures) []analysis.Metric {
	if f.State == wf.Deleted || f.State == wf.Missing {
		return nil
	}
	return []analysis.Metric{{Day: analysis.WorkoutDay, Name: "feature_active_seconds", Algorithm: wf.Algorithm, Version: wf.Version, Value: f.Timing.Active, Unit: "s", Quality: map[bool]string{true: "insufficient", false: "partial"}[f.Timing.Active == nil], Details: map[string]any{"artifactKey": wf.ArtifactKey}}, {Day: analysis.WorkoutDay, Name: "feature_preferred_distance", Algorithm: wf.Algorithm, Version: wf.Version, Value: f.Distance.Preferred, Unit: "m", Quality: map[bool]string{true: "insufficient", false: "partial"}[f.Distance.Preferred == nil], Details: map[string]any{"artifactKey": wf.ArtifactKey}}}
}

func logIdentity(value string) string {
	if value == "" {
		return ""
	}
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])
}
