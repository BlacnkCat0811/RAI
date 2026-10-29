//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"rai/server/internal/httpapi"
	"rai/server/internal/jobs"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
)

func trainingHTTP(t *testing.T, f *fixture, method, path string, b []byte) *httptest.ResponseRecorder {
	t.Helper()
	for attempt := 0; attempt < 4; attempt++ {
		r := httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		httpapi.New(f.s, analysisLog).ServeHTTP(w, r)
		if w.Code != 429 || attempt == 3 {
			return w
		}
		seconds, e := strconv.Atoi(w.Header().Get("Retry-After"))
		if e != nil || seconds < 1 || seconds > 10 {
			t.Fatal("invalid admission retry")
		}
		time.Sleep(time.Duration(seconds) * time.Second)
	}
	panic("unreachable")
}

func TestFullReviewTrainingJSONDrift(t *testing.T) {
	f, p := trainingFixture(t)
	ctx := context.Background()
	sid := p.Effective.Sessions[0].ID
	for _, name := range []string{"missing_expected_revision", "case_variant", "duplicate_key", "null_notes", "unknown", "type", "null_revision", "nested_case"} {
		t.Run(name, func(t *testing.T) {
			body := map[string]any{"commandID": syncing.NewID(), "overrideID": syncing.NewID(), "sessionID": sid, "operation": "edit", "path": "distanceM", "value": 7000, "expectedOverrideRevision": 0}
			switch name {
			case "missing_expected_revision":
				delete(body, "expectedOverrideRevision")
			case "case_variant":
				body["CommandID"] = body["commandID"]
				delete(body, "commandID")
			case "unknown":
				body["extra"] = true
			case "type":
				body["expectedOverrideRevision"] = "0"
			case "null_revision":
				body["expectedOverrideRevision"] = nil
			case "null_notes", "nested_case":
				newID := syncing.NewID()
				body["operation"] = "add"
				body["sessionID"] = newID
				delete(body, "path")
				delete(body, "value")
				manual := map[string]any{"sessionID": newID, "date": p.Effective.Sessions[0].Date, "type": "EASY", "distanceM": 1000, "notes": nil}
				if name == "nested_case" {
					delete(manual, "notes")
					manual["SessionID"] = manual["sessionID"]
					delete(manual, "sessionID")
				}
				body["session"] = manual
			}
			b, _ := json.Marshal(body)
			if name == "duplicate_key" {
				b = append([]byte(`{"value":6000,`), b[1:]...)
			}
			w := trainingHTTP(t, f, "POST", "/v2/training/plans/"+p.ID+"/overrides", b)
			if w.Code != 422 {
				t.Fatal("invalid wire accepted", w.Code)
			}
			var count int
			if e := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM training_overrides`).Scan(&count); e != nil || count != 0 {
				t.Fatal("invalid request persisted", e)
			}
		})
	}
	for revision, path := range []string{"targetHR", "paceSPerKM"} {
		body := map[string]any{"commandID": syncing.NewID(), "overrideID": syncing.NewID(), "sessionID": sid, "operation": "edit", "path": path, "value": nil, "expectedOverrideRevision": revision}
		b, _ := json.Marshal(body)
		url := "/v2/training/plans/" + p.ID + "/overrides"
		w := trainingHTTP(t, f, "POST", url, b)
		if w.Code != 200 {
			t.Fatal("legal clear rejected", w.Code)
		}
		// Whitespace and object key order cannot alter the durable command hash.
		var spaced bytes.Buffer
		if e := json.Indent(&spaced, b, "", "  "); e != nil {
			t.Fatal(e)
		}
		again := trainingHTTP(t, f, "POST", url, spaced.Bytes())
		if again.Code != 200 || !bytes.Equal(w.Body.Bytes(), again.Body.Bytes()) {
			t.Fatal("canonical command replay changed")
		}
		body["value"] = 155
		b, _ = json.Marshal(body)
		if trainingHTTP(t, f, "POST", url, b).Code != 409 {
			t.Fatal("conflicting command accepted")
		}
	}
}

func TestFullReviewTrainingQueryBound(t *testing.T) {
	for _, years := range []int{1, 3, 5} {
		t.Run(fmt.Sprintf("%dy", years), func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			density := 8
			seedAnalysisHistoryDensity(t, f, 365*years, density)
			day := time.Now().In(time.FixedZone("UTC+8", 8*3600)).Format("2006-01-02")
			start, end, e := postgres.TrainingWindow("Asia/Shanghai", day)
			if e != nil {
				t.Fatal(e)
			}
			var raw []byte
			if e = f.s.Pool.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+postgres.TrainingWorkoutsSQL, f.p.DatasetID, "Asia/Shanghai", start, end).Scan(&raw); e != nil {
				t.Fatal(e)
			}
			var plan []map[string]any
			if e = json.Unmarshal(raw, &plan); e != nil {
				t.Fatal(e)
			}
			root := plan[0]["Plan"].(map[string]any)
			scanned := float64(0)
			timeIndex := false
			var visit func(map[string]any)
			visit = func(n map[string]any) {
				if n["Relation Name"] == "workouts" {
					scanned += (n["Actual Rows"].(float64) + numberOrZero(n["Rows Removed by Filter"])) * n["Actual Loops"].(float64)
					if strings.Contains(fmt.Sprint(n["Index Cond"]), "event_start") {
						timeIndex = true
					}
				}
				if xs, ok := n["Plans"].([]any); ok {
					for _, x := range xs {
						visit(x.(map[string]any))
					}
				}
			}
			visit(root)
			if root["Actual Rows"].(float64) != float64(28*density) || scanned > float64(29*density) || !timeIndex {
				t.Fatal("recent query scans history", scanned, timeIndex)
			}
			t.Logf("TRAINING_QUERY_BASELINE years=%d fixture_rows=%d returned=%v scanned_workouts=%v shared_hit_blocks=%v execution_ms=%v", years, 365*years*density, root["Actual Rows"], scanned, root["Shared Hit Blocks"], plan[0]["Execution Time"])
			t.Log("TRAINING_QUERY_PLAN=" + string(raw))
			// A late explicit association to an older Workout uses the identity path.
			var oldest string
			if e = f.s.Pool.QueryRow(ctx, `SELECT id::text FROM p4_history ORDER BY at,id LIMIT 1`).Scan(&oldest); e != nil {
				t.Fatal(e)
			}
			p, e := f.s.CreateTrainingPlan(ctx, f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: syncing.NewID(), Config: trainingConfig()})
			if e != nil {
				t.Fatal(e)
			}
			runTraining(t, f)
			p, e = f.s.TrainingPlan(ctx, f.p, p.ID)
			if e != nil {
				t.Fatal(e)
			}
			ex, e := f.s.StartTrainingExecution(ctx, f.p, training.StartRequest{CommandID: syncing.NewID(), ExecutionID: syncing.NewID(), PlanID: p.ID, EffectiveID: p.Effective.ID, SessionID: p.Effective.Sessions[0].ID})
			if e != nil {
				t.Fatal(e)
			}
			ex, e = f.s.ChangeTrainingExecution(ctx, f.p, ex.ID, training.ExecutionChange{CommandID: syncing.NewID(), Operation: "complete", ExpectedRevision: 1})
			if e != nil {
				t.Fatal(e)
			}
			_, e = f.s.ChangeTrainingExecution(ctx, f.p, ex.ID, training.ExecutionChange{CommandID: syncing.NewID(), Operation: "associate", ExpectedRevision: 2, WorkoutID: oldest})
			if e != nil {
				t.Fatal(e)
			}
			l := claimTraining(t, f)
			in, e := f.s.LoadTraining(ctx, l, time.Now().UTC())
			if e != nil {
				t.Fatal(e)
			}
			if len(in.Context.Workouts) != 28*density || in.Workouts[oldest].ID != oldest {
				t.Fatal("late association or window lost")
			}
			jobs.Process(ctx, f.s, l, analysisLog)
		})
	}
}
func numberOrZero(v any) float64 {
	if n, ok := v.(float64); ok {
		return n
	}
	return 0
}

func TestFullReviewAuditResponseBudget(t *testing.T) {
	f, p := trainingFixture(t)
	ctx := context.Background()
	sid := p.Effective.Sessions[0].ID
	for i := 0; i < 256; i++ {
		editTraining(t, f, p, sid, "notes", strings.Repeat("x", 2000), int64(i))
	}
	latest, e := f.s.TrainingPlan(ctx, f.p, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	original, e := f.s.TrainingAudit(ctx, f.p, p.ID, latest.Effective.ID)
	if e != nil {
		t.Fatal(e)
	}
	path := "/v2/training/plans/" + p.ID + "/effective/" + latest.Effective.ID
	legacy := trainingHTTP(t, f, "GET", path, nil)
	if legacy.Code != 413 || legacy.Header().Get("Link") == "" {
		t.Fatal("oversized legacy response not redirected explicitly")
	}
	var collected []training.Override
	after := int64(0)
	pages := 0
	maxBytes := 0
	for {
		w := trainingHTTP(t, f, "GET", fmt.Sprintf("%s/audit?after=%d&limit=20", path, after), nil)
		if w.Code != 200 || w.Body.Len() > training.PageBudget {
			t.Fatal("audit page unreadable", w.Code, w.Body.Len())
		}
		if w.Body.Len() > maxBytes {
			maxBytes = w.Body.Len()
		}
		var page training.AuditPage
		if e = json.Unmarshal(w.Body.Bytes(), &page); e != nil {
			t.Fatal(e)
		}
		if page.Version.ID != latest.Effective.ID || page.Version.OverrideRevision != 256 {
			t.Fatal("audit snapshot drift")
		}
		collected = append(collected, page.Overrides...)
		pages++
		if page.Next == 0 {
			break
		}
		if page.Next <= after || pages > 256 {
			t.Fatal("non-progressing cursor")
		}
		after = page.Next
	}
	if !reflect.DeepEqual(collected, original.Overrides) {
		t.Fatal("immutable audit incomplete")
	}
	t.Logf("AUDIT_RESPONSE_BASELINE overrides=256 page_limit=20 pages=%d max_bytes=%d final_cursor=0", pages, maxBytes)
	// Reconstruct every array independently and replay the entire immutable view.
	var reconstructed training.Audit
	for _, section := range []string{"effective_sessions", "base_sessions", "context_workouts", "context_signals", "context_feedback"} {
		cursor := int64(0)
		items := []json.RawMessage{}
		for {
			d, e := f.s.TrainingDetailPage(ctx, f.p, p.ID, latest.Effective.ID, section, cursor, 20)
			if e != nil {
				t.Fatal(e)
			}
			if len(trainingJSONForTest(d))+1 > training.PageBudget {
				t.Fatal("detail over budget")
			}
			if reconstructed.Effective.ID == "" {
				if e = json.Unmarshal(d.Metadata, &reconstructed); e != nil {
					t.Fatal(e)
				}
			}
			items = append(items, d.Items...)
			if d.Next == 0 {
				break
			}
			if d.Next <= cursor {
				t.Fatal("detail cursor stalled")
			}
			cursor = d.Next
		}
		b, _ := json.Marshal(items)
		switch section {
		case "effective_sessions":
			e = json.Unmarshal(b, &reconstructed.Effective.Sessions)
		case "base_sessions":
			e = json.Unmarshal(b, &reconstructed.Base.Sessions)
		case "context_workouts":
			e = json.Unmarshal(b, &reconstructed.Context.Workouts)
		case "context_signals":
			e = json.Unmarshal(b, &reconstructed.Context.Signals)
		case "context_feedback":
			e = json.Unmarshal(b, &reconstructed.Context.Feedback)
		}
		if e != nil {
			t.Fatal(e)
		}
	}
	replay, e := training.ResolveEffectiveVersion(reconstructed.Base, collected, reconstructed.Effective.MergeVersion)
	if e != nil || !reflect.DeepEqual(replay, reconstructed.Effective.Base) || !reflect.DeepEqual(reconstructed.Context, original.Context) {
		t.Fatal("paged replay differs", e)
	}
	_, e = f.s.PutTrainingOverride(ctx, f.p, p.ID, training.OverrideRequest{CommandID: syncing.NewID(), OverrideID: syncing.NewID(), SessionID: sid, Operation: "revoke", TargetID: collected[0].ID, ExpectedOverrideRevision: 256})
	if e != nil {
		t.Fatal(e)
	}
	pinned, e := f.s.TrainingAuditPage(ctx, f.p, p.ID, latest.Effective.ID, 0, 1)
	if e != nil || pinned.Overrides[0].Status != "active" {
		t.Fatal("later revoke changed old version")
	}
	current, e := f.s.TrainingPlan(ctx, f.p, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	newPage, e := f.s.TrainingAuditPage(ctx, f.p, p.ID, current.Effective.ID, 0, 1)
	if e != nil || newPage.Overrides[0].Status != "revoked" {
		t.Fatal("revoke status missing")
	}
}
func trainingJSONForTest(v any) []byte {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return b
}

func TestFullReviewHistoryResponseBudget(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	cfg := trainingConfig()
	cfg.Weeks = 16
	p, e := f.s.CreateTrainingPlan(ctx, f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: syncing.NewID(), Config: cfg})
	if e != nil {
		t.Fatal(e)
	}
	runTraining(t, f)
	for i := 0; i < 49; i++ {
		recalcTraining(t, f, p)
		runTraining(t, f)
	}
	all := map[string]bool{}
	after := int64(0)
	pages := 0
	maxBytes := 0
	for {
		w := trainingHTTP(t, f, "GET", fmt.Sprintf("/v2/training/plans/%s/history/versions?limit=13&after=%d", p.ID, after), nil)
		if w.Code != 200 || w.Body.Len() > training.PageBudget {
			t.Fatal("history unreadable", w.Code)
		}
		if w.Body.Len() > maxBytes {
			maxBytes = w.Body.Len()
		}
		if bytes.Contains(w.Body.Bytes(), []byte(`"sessions"`)) {
			t.Fatal("history is not a summary")
		}
		var page training.History
		if e = json.Unmarshal(w.Body.Bytes(), &page); e != nil {
			t.Fatal(e)
		}
		for _, v := range page.Versions {
			if all[v.ID] {
				t.Fatal("duplicate version")
			}
			all[v.ID] = true
			if v.Number <= after {
				t.Fatal("unstable ordering")
			}
		}
		pages++
		if page.Next == 0 {
			break
		}
		if page.Next <= after || pages > 50 {
			t.Fatal("cursor stalled")
		}
		after = page.Next
	}
	if len(all) != 50 {
		t.Fatal("history incomplete")
	}
	legacy := trainingHTTP(t, f, "GET", fmt.Sprintf("/v2/training/plans/%s/history?limit=50", p.ID), nil)
	if legacy.Code != 413 || legacy.Header().Get("Link") == "" {
		t.Fatal("legacy history did not expose migration path")
	}
	t.Logf("HISTORY_RESPONSE_BASELINE weeks=16 versions=50 page_limit=13 pages=%d max_bytes=%d final_cursor=0", pages, maxBytes)
}

func TestTrainingAuditByteLimitAdjustsItemCount(t *testing.T) {
	f, p := trainingFixture(t)
	ctx := context.Background()
	sid := p.Effective.Sessions[0].ID
	for i := 0; i < 24; i++ {
		editTraining(t, f, p, sid, "notes", strings.Repeat("\x01", 2000), int64(i))
	}
	p, e := f.s.TrainingPlan(ctx, f.p, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	after := int64(0)
	count := 0
	pages := 0
	for {
		page, e := f.s.TrainingAuditPage(ctx, f.p, p.ID, p.Effective.ID, after, 20)
		if e != nil {
			t.Fatal(e)
		}
		if len(trainingJSONForTest(page))+1 > training.PageBudget {
			t.Fatal("escaped strings exceeded byte budget")
		}
		if pages == 0 && (len(page.Overrides) >= 20 || page.Next == 0) {
			t.Fatal("count limit did not shrink to byte budget")
		}
		count += len(page.Overrides)
		pages++
		if page.Next == 0 {
			break
		}
		if page.Next <= after {
			t.Fatal("byte cursor stalled")
		}
		after = page.Next
	}
	if count != 24 {
		t.Fatal("byte paging lost audit facts")
	}
}

func TestTrainingLargeVersionContextDetailBudget(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p, e := f.s.CreateTrainingPlan(ctx, f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: syncing.NewID(), Config: trainingConfig()})
	if e != nil {
		t.Fatal(e)
	}
	l := claimTraining(t, f)
	in, e := f.s.LoadTraining(ctx, l, time.Now().UTC())
	if e != nil {
		t.Fatal(e)
	}
	// Prebuild a large, published synthetic context to isolate the read budget
	// from high-density loader timing. This is a size fixture, not a throughput
	// or 3584-Workout Worker acceptance claim.
	seedAnalysisHistoryDensity(t, f, 28, 128)
	rows, e := f.s.Pool.Query(ctx, `SELECT id::text,run::text,(at AT TIME ZONE 'Asia/Shanghai')::date::text FROM p4_history ORDER BY at,id`)
	if e != nil {
		t.Fatal(e)
	}
	for rows.Next() {
		var id, run, day string
		if e = rows.Scan(&id, &run, &day); e != nil {
			t.Fatal(e)
		}
		in.Context.Workouts = append(in.Context.Workouts, training.Workout{ID: id, RunID: run, Generation: 1, Date: day, Load: training.Ptr(100), Metrics: []training.Signal{{RunID: run, Generation: 1, Day: day, Name: "workout_load", Algorithm: "training_load.trimp", Version: "v1", Quality: "available", Value: training.Ptr(100)}}})
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		t.Fatal(e)
	}
	run, e := f.s.StartTrainingRun(ctx, l, in)
	if e != nil {
		t.Fatal(e)
	}
	base, e := training.Generate(in.Context)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.PublishTraining(ctx, l, run, in, base); e != nil {
		t.Fatal(e)
	}
	p, e = f.s.TrainingPlan(ctx, f.p, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	path := "/v2/training/plans/" + p.ID + "/effective/" + p.Effective.ID
	if trainingHTTP(t, f, "GET", path, nil).Code != 413 {
		t.Fatal("large single version should require section pages")
	}
	cursor := int64(0)
	count := 0
	pages := 0
	for {
		page, e := f.s.TrainingDetailPage(ctx, f.p, p.ID, p.Effective.ID, "context_workouts", cursor, 50)
		if e != nil {
			t.Fatal(e)
		}
		if len(trainingJSONForTest(page))+1 > training.PageBudget {
			t.Fatal("large context page over budget")
		}
		count += len(page.Items)
		pages++
		if page.Next == 0 {
			break
		}
		if page.Next <= cursor {
			t.Fatal("context cursor stalled")
		}
		cursor = page.Next
	}
	if count != 3584 {
		t.Fatal("large context incomplete")
	}
	other := setup(t)
	if _, e = f.s.TrainingDetailPage(ctx, other.p, p.ID, p.Effective.ID, "context_workouts", 0, 20); e == nil {
		t.Fatal("cross dataset detail leaked")
	}
	if _, e = f.s.TrainingAuditPage(ctx, other.p, p.ID, p.Effective.ID, 0, 20); e == nil {
		t.Fatal("cross dataset audit leaked")
	}
	t.Logf("LARGE_VERSION_DETAIL_BASELINE context_workouts=%d pages=%d complete=true", count, pages)
}
