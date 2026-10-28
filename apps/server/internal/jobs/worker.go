package jobs

import (
	"context"
	"log/slog"
	"time"

	"rai/server/internal/analysis"
	"rai/server/internal/training"
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

var kinds = []string{"analysis.workout", "analysis.sample", "analysis.series", "analysis.training_state", "training.plan"}

// Run uses one processing slot. The short poll waits on cancellation and the
// independent renewal loop cancels calculation immediately after lease loss.
func Run(ctx context.Context, q Queue, log *slog.Logger) error {
	log.Info("worker_started", "handlers", len(kinds), "processing_slots", 1)
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	lastRecovery := time.Time{}
	for {
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(lastRecovery) >= 15*time.Second {
			n, e := q.RequeueExpired(ctx)
			if e != nil && ctx.Err() == nil {
				log.Warn("queue_recovery_failed", "reason", "database_unavailable")
			}
			if n > 0 {
				log.Info("leases_recovered", "count", n)
			}
			if e = q.AdvanceAnalysisDay(ctx, time.Now().UTC()); e != nil && ctx.Err() == nil {
				log.Warn("analysis_day_advance_failed", "reason", "database_unavailable")
			}
			lastRecovery = time.Now()
		}
		l, e := q.Claim(ctx, kinds)
		if e == nil {
			Process(ctx, q, l, log)
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-poll.C:
		}
	}
}

// Process is also the deterministic single-claim integration/administrative
// harness. Algorithms remain in analysis; storage owns publication transactions.
func Process(ctx context.Context, q Queue, l analysis.Lease, log *slog.Logger) {
	processWithRenewal(ctx, q, l, log, 20*time.Second)
}
func processWithRenewal(ctx context.Context, q Queue, l analysis.Lease, log *slog.Logger, renewEvery time.Duration) {
	start := time.Now()
	name, _ := analysis.Algorithm(l.Kind)
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
	if l.Kind == "training.plan" {
		status := "FAILED"
		var in training.Input
		id := ""
		var err error
		tq, ok := q.(TrainingQueue)
		if ok {
			in, err = tq.LoadTraining(runCtx, l, time.Now().UTC())
			if err == nil {
				id, err = tq.StartTrainingRun(runCtx, l, in)
			}
			var base training.Base
			if err == nil && in.Generation == l.TargetGeneration {
				base, err = training.Generate(in.Context)
			}
			if err == nil {
				status, err = tq.PublishTraining(runCtx, l, id, in, base)
			}
		}
		cancel()
		<-renewalDone
		if err != nil {
			status = "FAILED"
		}
		if ok && err != nil && ctx.Err() == nil {
			failCtx, c := context.WithTimeout(ctx, 5*time.Second)
			_ = tq.FailTraining(failCtx, l, id, err)
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
			}
		}
		log.Info("training_execution", "plan_id", trainingPlanFromScope(l.ScopeKey), "generation", l.TargetGeneration, "base_version", baseID, "effective_version", effectiveID, "job_id", l.ID, "run_id", id, "generator_version", training.Generator, "override_count", overrideCount, "duration_ms", time.Since(start).Milliseconds(), "status", status)
		return
	}
	in, err := q.LoadAnalysis(runCtx, l, time.Now().UTC())
	id := ""
	if err != nil && in.Generation > 0 {
		in.InputVersion += "/incomplete"
		id, _ = q.StartAnalysisRun(runCtx, l, in)
	}
	status := "FAILED"
	if err == nil {
		id, err = q.StartAnalysisRun(runCtx, l, in)
	}
	var ms []analysis.Metric
	if err == nil && in.Generation == l.TargetGeneration {
		ms, err = analysis.Analyze(runCtx, l.Kind, in)
	}
	if err == nil {
		status, err = q.PublishAnalysis(runCtx, l, id, in, ms)
	}
	cancel()
	<-renewalDone
	if err != nil && ctx.Err() == nil {
		status = "FAILED"
		code, retryable := analysis.ErrorCode(err)
		failCtx, c := context.WithTimeout(ctx, 5*time.Second)
		_ = q.FailAnalysis(failCtx, l, id, code, retryable)
		c()
	}
	log.Info("analysis_execution", "job_id", l.ID, "scope_key", l.ScopeKey, "generation", l.TargetGeneration, "algorithm", name, "algorithm_version", analysis.Version, "run_id", id, "duration_ms", time.Since(start).Milliseconds(), "result_status", status)
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
