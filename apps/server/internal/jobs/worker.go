package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"time"

	"rai/server/internal/analysis"
	"rai/server/internal/modelmanifest"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
)

type Queue interface {
	RequeueExpired(context.Context) (int, error)
	Claim(context.Context, []string) (analysis.Lease, error)
	Renew(context.Context, analysis.Lease) error
	FailAnalysis(context.Context, analysis.Lease, string, string, bool) error
	AdvanceAnalysisDay(context.Context, time.Time) error
}

// recoveryInterval is how often leases are recovered and the analysis day is
// advanced. It is a variable only so tests can shorten it.
var recoveryInterval = 15 * time.Second

// The worker claims only the v2 model stack and training plans. The legacy
// analysis kinds (workout bundle, training_state, v1 candidate models) are
// retired: schema 53 no longer queues them.
var kinds = []string{"training.plan", modelmanifest.StackJobKind}

// Run uses one processing slot. The short poll waits on cancellation and the
// independent renewal loop cancels calculation immediately after lease loss.
//
// Run pins the whole run to one explicit as-of instant. It is only meant for
// deterministic harnesses; a long-lived worker must use RunWithClock, otherwise
// day advancement never moves past the instant the process started.
func Run(ctx context.Context, q Queue, log *slog.Logger, asOf time.Time) error {
	return RunWithClock(ctx, q, log, func() time.Time { return asOf })
}

// RunWithClock reads the clock again for every day advancement and every
// claimed job. One job still sees a single instant, so its calculation stays
// deterministic, but the instant moves forward between jobs and across
// midnight.
func RunWithClock(ctx context.Context, q Queue, log *slog.Logger, now func() time.Time) error {
	log.Info("worker_started", "handlers", len(kinds), "processing_slots", 1)
	go runWorkoutIngests(ctx, q, log)
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	recovery := time.NewTicker(recoveryInterval)
	defer recovery.Stop()
	recoverExpired := func() {
		n, e := q.RequeueExpired(ctx)
		if e != nil && ctx.Err() == nil {
			log.Warn("queue_recovery_failed", "reason", "database_unavailable")
		}
		if n > 0 {
			log.Info("leases_recovered", "count", n)
		}
		if e = q.AdvanceAnalysisDay(ctx, now().UTC()); e != nil && ctx.Err() == nil {
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
		claimStart := time.Now()
		l, e := q.Claim(ctx, kinds)
		if e == nil {
			log.Info("job_claim", "component", "worker", "operation", "claim", "status", "claimed", "duration_ms", time.Since(claimStart).Milliseconds())
			Process(ctx, q, l, log, now().UTC())
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
		log.Info("training_execution", "component", "worker", "operation", "training", "error_code", errorCode, "plan_hash", logIdentity(trainingPlanFromScope(l.ScopeKey)), "generation", l.TargetGeneration, "base_hash", logIdentity(baseID), "effective_hash", logIdentity(effectiveID), "job_hash", logIdentity(l.ID), "run_hash", logIdentity(id), "generator_version", training.GeneratorFor(in.Plan.Config), "override_count", overrideCount, "duration_ms", elapsed().Milliseconds(), "publication_ms", publicationDuration.Milliseconds(), "status", status)
		return
	}
	code := "analysis_unsupported_algorithm"
	if algorithmErr != nil {
		code, _ = analysis.ErrorCode(algorithmErr)
	}
	failCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	if failErr := q.FailAnalysis(failCtx, l, "", code, false); failErr != nil && ctx.Err() == nil {
		log.Warn("analysis_failure_record_failed", "reason", "database_unavailable")
	}
	stop()
	cancel()
	<-renewalDone
	log.Info("analysis_execution", "component", "worker", "operation", "analysis", "error_code", code, "job_hash", logIdentity(l.ID), "scope_hash", logIdentity(l.ScopeKey), "generation", l.TargetGeneration, "algorithm", name, "duration_ms", elapsed().Milliseconds(), "status", "FAILED", "result_status", "FAILED")
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

func logIdentity(value string) string {
	if value == "" {
		return ""
	}
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])
}
