//go:build integration

package integration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
)

func TestConcurrentJobClaimAndCrashRecovery(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.commit(t, []syncing.Event{sample(syncing.NewID(), 120)}, 1, nil)
	type result struct {
		lease postgres.Lease
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			l, err := f.s.Claim(ctx, []string{"analysis.sample"})
			results <- result{l, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var first postgres.Lease
	wins, empty := 0, 0
	for r := range results {
		if r.err == nil {
			wins++
			first = r.lease
		} else if errors.Is(r.err, pgx.ErrNoRows) {
			empty++
		} else {
			t.Fatal(r.err)
		}
	}
	if wins != 1 || empty != 1 {
		t.Fatalf("claim results: %d wins, %d empty", wins, empty)
	}
	if _, err := f.s.Pool.Exec(ctx, `UPDATE jobs SET lease_until=now()-interval '1 second' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := f.s.RequeueExpired(ctx); err != nil || n != 1 {
		t.Fatalf("recovery: %d, %v", n, err)
	}
	if _, err := f.s.Pool.Exec(ctx, `UPDATE jobs SET run_after=now() WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := f.s.Claim(ctx, []string{"analysis.sample"})
	if err != nil || second.ID != first.ID || second.FencingToken <= first.FencingToken {
		t.Fatalf("reclaim: %v", err)
	}
	requireCode(t, f.s.Fail(ctx, first, true), "lease_lost")
	requireCode(t, f.s.Complete(ctx, first), "lease_lost")
}

func TestJobFailureRetryAndAttemptLimit(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.commit(t, []syncing.Event{sample(syncing.NewID(), 120)}, 1, nil)
	for i := 1; i <= 8; i++ {
		started := time.Now()
		l, err := f.s.Claim(ctx, []string{"analysis.sample"})
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			t.Logf("synthetic job claim: %s", time.Since(started))
		}
		if err = f.s.Fail(ctx, l, true); err != nil {
			t.Fatal(err)
		}
		var state, code string
		var attempts, finished int
		err = f.s.Pool.QueryRow(ctx, `SELECT state,last_error_code,attempts,
			(SELECT count(*) FROM job_attempts WHERE job_id=jobs.id AND finished_at IS NOT NULL)
			FROM jobs WHERE id=$1`, l.ID).Scan(&state, &code, &attempts, &finished)
		if err != nil {
			t.Fatal(err)
		}
		want := "queued"
		if i == 8 {
			want = "failed"
		}
		if state != want || code != "handler_retryable" || attempts != i || finished != i {
			t.Fatalf("attempt %d: %s %s %d %d", i, state, code, attempts, finished)
		}
		if _, err = f.s.Pool.Exec(ctx, `UPDATE jobs SET run_after=now() WHERE id=$1`, l.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.Claim(ctx, []string{"analysis.sample"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("failed job claim: %v", err)
	}
}

func TestPermanentFailureAndAttemptUpdateAtomicity(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.commit(t, []syncing.Event{sample(syncing.NewID(), 120)}, 1, nil)
	l, err := f.s.Claim(ctx, []string{"analysis.sample"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(ctx, `DELETE FROM job_attempts WHERE job_id=$1 AND fencing_token=$2`, l.ID, l.FencingToken); err != nil {
		t.Fatal(err)
	}
	if err = f.s.Fail(ctx, l, false); err == nil {
		t.Fatal("missing attempt accepted")
	}
	var state string
	if err = f.s.Pool.QueryRow(ctx, `SELECT state FROM jobs WHERE id=$1`, l.ID).Scan(&state); err != nil || state != "running" {
		t.Fatalf("job update escaped rollback: %s %v", state, err)
	}
	if _, err = f.s.Pool.Exec(ctx, `INSERT INTO job_attempts(job_id,fencing_token) VALUES($1,$2)`, l.ID, l.FencingToken); err != nil {
		t.Fatal(err)
	}
	if err = f.s.Fail(ctx, l, false); err != nil {
		t.Fatal(err)
	}
	var code string
	if err = f.s.Pool.QueryRow(ctx, `SELECT state,last_error_code FROM jobs WHERE id=$1`, l.ID).Scan(&state, &code); err != nil || state != "failed" || code != "handler_failed" {
		t.Fatalf("permanent failure: %s %s %v", state, code, err)
	}
	if _, err = f.s.Pool.Exec(ctx, `UPDATE jobs SET attempts=-1 WHERE id=$1`, l.ID); err == nil {
		t.Fatal("negative attempt count accepted")
	}
	if _, err = f.s.Pool.Exec(ctx, `UPDATE jobs SET fencing_token=-1 WHERE id=$1`, l.ID); err == nil {
		t.Fatal("negative fencing token accepted")
	}
}
