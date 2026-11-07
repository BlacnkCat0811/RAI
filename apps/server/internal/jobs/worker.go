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
		claimStart := time.Now()
		l, e := q.Claim(ctx, kinds)
		if e == nil {
			log.Info("job_claim", "component", "worker", "operation", "claim", "status", "claimed", "duration_ms", time.Since(claimStart).Milliseconds())
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
	if l.Kind == modelmanifest.StackJobKind {
		status := "FAILED"
		var err error
		if processor, ok := q.(interface {
			ProcessModelStack(context.Context, analysis.Lease, time.Time) (string, error)
		}); ok {
			status, err = processor.ProcessModelStack(runCtx, l, time.Now())
		} else {
			err = &analysis.Failure{Code: "analysis_unsupported_algorithm"}
		}
		cancel()
		<-renewalDone
		if err != nil && ctx.Err() == nil {
			status = "FAILED"
			code, retryable := analysis.ErrorCode(err)
			failCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			_ = q.FailAnalysis(failCtx, l, "", code, retryable)
			stop()
		}
		log.Info("model_stack_execution", "component", "worker", "status", status, "duration_ms", time.Since(start).Milliseconds())
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
			in, err = tq.LoadTraining(runCtx, l, time.Now().UTC())
			if err == nil {
				id, err = tq.StartTrainingRun(runCtx, l, in)
			}
			var base training.Base
			if err == nil && in.Generation == l.TargetGeneration {
				base, err = training.Generate(in.Context)
			}
			if err == nil {
				publicationStart := time.Now()
				status, err = tq.PublishTraining(runCtx, l, id, in, base)
				publicationDuration = time.Since(publicationStart)
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
		errorCode := ""
		if err != nil {
			errorCode = "training_failed"
		}
		log.Info("training_execution", "component", "worker", "operation", "training", "error_code", errorCode, "plan_hash", logIdentity(trainingPlanFromScope(l.ScopeKey)), "generation", l.TargetGeneration, "base_hash", logIdentity(baseID), "effective_hash", logIdentity(effectiveID), "job_hash", logIdentity(l.ID), "run_hash", logIdentity(id), "generator_version", training.Generator, "override_count", overrideCount, "duration_ms", time.Since(start).Milliseconds(), "publication_ms", publicationDuration.Milliseconds(), "status", status)
		return
	}
	snapshotStart := time.Now()
	in, err := q.LoadAnalysis(runCtx, l, time.Now().UTC())
	snapshotDuration := time.Since(snapshotStart)
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
	publicationDuration := time.Duration(0)
	algorithmDuration := time.Duration(0)
	if err == nil && in.Generation == l.TargetGeneration {
		algorithmStart := time.Now()
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
		algorithmDuration = time.Since(algorithmStart)
	}
	if err == nil {
		publicationStart := time.Now()
		status, err = q.PublishAnalysis(runCtx, l, id, in, ms)
		publicationDuration = time.Since(publicationStart)
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
	errorCode := ""
	if err != nil {
		errorCode, _ = analysis.ErrorCode(err)
	}
	log.Info("analysis_execution", "component", "worker", "operation", "analysis", "error_code", errorCode, "job_hash", logIdentity(l.ID), "scope_hash", logIdentity(l.ScopeKey), "generation", l.TargetGeneration, "algorithm", name, "algorithm_version", analysis.BundleVersion(l.Kind), "snapshot_ms", float64(snapshotDuration.Microseconds())/1000, "algorithm_ms", float64(algorithmDuration.Microseconds())/1000, "run_hash", logIdentity(id), "duration_ms", time.Since(start).Milliseconds(), "publication_ms", publicationDuration.Milliseconds(), "status", status, "result_status", status)
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
