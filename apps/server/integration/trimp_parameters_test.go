//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"rai/server/internal/httpapi"
)

func TestTRIMPParametersPublicInput(t *testing.T) {
	f := setup(t)
	api, _ := b4Roles(t, f)
	handler := httpapi.New(api, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	var functionBody string
	if err := f.s.Pool.QueryRow(ctx, `SELECT pg_get_functiondef('rai_commit_typed_model_input(uuid,uuid,uuid,text,timestamptz,timestamptz,uuid,uuid,text,bytea,jsonb,text)'::regprocedure)`).Scan(&functionBody); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(functionBody, "parameterVersion") {
		t.Fatal("migrated function body lacks the trimp receipt branch")
	}

	if _, err := api.Pool.Exec(ctx,
		`INSERT INTO analysis_parameter_sets(dataset_id,version,resting_hr,max_hr,coefficient_a,coefficient_b,provenance) VALUES($1,99,60,190,1,1,'api bypass')`,
		f.p.DatasetID); err == nil {
		t.Fatal("api role must not write analysis_parameter_sets directly")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("expected insufficient_privilege for parameter insert, got %v", err)
		}
	}

	firstID := "a1f9619f-8b86-4d01-b42d-00cf4fc96401"
	secondID := "a1f9619f-8b86-4d01-b42d-00cf4fc96402"
	for index, workoutID := range []string{firstID, secondID} {
		at := time.Now().UTC().Add(-time.Duration(index+1) * time.Hour)
		if _, err := f.s.Pool.Exec(ctx, `INSERT INTO workouts(dataset_id,workout_uuid,event_start,event_end,activity_type,duration_us,source_bundle_id,content_sha256,entity_revision,payload)
 VALUES($1,$2::uuid,$3::timestamptz,$4::timestamptz,37,3600000000,'synthetic.trimp',sha256(convert_to($2::uuid::text,'UTF8')),1,
 jsonb_build_object('op','upsert','kind','workout','type','HKWorkoutTypeIdentifier','uuid',$2::uuid::text,'startUs',(extract(epoch from $3::timestamptz)*1000000)::bigint,'endUs',(extract(epoch from $4::timestamptz)*1000000)::bigint,'source',jsonb_build_object('bundleID','synthetic.trimp'),'workout',jsonb_build_object('activityType',37,'durationUs',3600000000,'totalDistance',jsonb_build_object('value',10000,'unit','m'))))`,
			f.p.DatasetID, workoutID, at, at.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	id := "7f9619ff-8b86-4d01-b42d-00cf4fc964ff"
	body := `{"id":"` + id + `","kind":"trimp_parameters","subjectKey":"athlete","effectiveFrom":"` +
		time.Now().Add(-time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339) +
		`","provenance":"ios_user_entered","payload":{"restingHR":52,"maxHR":188,"coefficientA":0.64,"coefficientB":1.92}}`
	code, first := postJSONHTTP(t, handler, f.enrollment.Token, "/v2/model-inputs/trimp-parameters", []byte(body))
	if code != 200 {
		t.Fatal("trimp input status", code, string(first))
	}
	var receipt map[string]any
	if err := json.Unmarshal(first, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["inputID"] != id || receipt["schemaVersion"] != "rai.trimp_parameters/1" ||
		receipt["parameterVersion"] != float64(1) || receipt["workoutCount"] != float64(2) {
		var parameterRows int
		var storedReceipt string
		_ = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM analysis_parameter_sets WHERE dataset_id=$1`, f.p.DatasetID).Scan(&parameterRows)
		_ = f.s.Pool.QueryRow(ctx, `SELECT receipt::text FROM model_input_receipts WHERE dataset_id=$1 AND input_id=$2`, f.p.DatasetID, id).Scan(&storedReceipt)
		t.Fatalf("trimp receipt fields rows=%d stored=%s receipt=%v", parameterRows, storedReceipt, receipt)
	}

	var version int64
	var rest, maxHR, coefficientA, coefficientB float64
	var provenance string
	if err := f.s.Pool.QueryRow(ctx, `SELECT version,resting_hr,max_hr,coefficient_a,coefficient_b,provenance
 FROM analysis_parameter_sets WHERE dataset_id=$1 ORDER BY version DESC LIMIT 1`, f.p.DatasetID).
		Scan(&version, &rest, &maxHR, &coefficientA, &coefficientB, &provenance); err != nil {
		t.Fatal(err)
	}
	if version != 1 || rest != 52 || maxHR != 188 || coefficientA != 0.64 || coefficientB != 1.92 || provenance != "ios_user_entered" {
		t.Fatalf("parameter row version=%d rest=%v max=%v a=%v b=%v provenance=%q", version, rest, maxHR, coefficientA, coefficientB, provenance)
	}

	for _, workoutID := range []string{firstID, secondID} {
		var generation int64
		var jobs int
		scope := "workout:HKWorkoutTypeIdentifier:" + workoutID
		if err := f.s.Pool.QueryRow(ctx, `SELECT generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, scope).Scan(&generation); err != nil {
			t.Fatal(err)
		}
		if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE dataset_id=$1 AND scope_key=$2 AND kind='analysis.workout' AND target_generation=$3`, f.p.DatasetID, scope, generation).Scan(&jobs); err != nil {
			t.Fatal(err)
		}
		if generation != 1 || jobs != 1 {
			t.Fatalf("scope %s generation=%d jobs=%d", scope, generation, jobs)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v2/analysis/parameters", nil)
	req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal("parameters read status", rec.Code, rec.Body.String())
	}
	var params struct {
		Parameters *struct {
			Version      int64   `json:"version"`
			RestingHR    float64 `json:"restingHR"`
			MaxHR        float64 `json:"maxHR"`
			CoefficientA float64 `json:"coefficientA"`
			CoefficientB float64 `json:"coefficientB"`
			Provenance   string  `json:"provenance"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &params); err != nil || params.Parameters == nil {
		t.Fatal("parameters read body", rec.Body.String(), err)
	}
	if params.Parameters.Version != 1 || params.Parameters.RestingHR != 52 || params.Parameters.MaxHR != 188 ||
		params.Parameters.CoefficientA != 0.64 || params.Parameters.CoefficientB != 1.92 || params.Parameters.Provenance != "ios_user_entered" {
		t.Fatalf("parameters read values %+v", params.Parameters)
	}

	time.Sleep(2100 * time.Millisecond) // preserve the default upload-lane budget
	code, replay := postJSONHTTP(t, handler, f.enrollment.Token, "/v2/model-inputs/trimp-parameters", []byte(body))
	if code != 200 || string(replay) != string(first) {
		t.Fatal("trimp replay must return the original receipt", code, string(replay))
	}
	var versions int
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM analysis_parameter_sets WHERE dataset_id=$1`, f.p.DatasetID).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 1 {
		t.Fatalf("replay appended parameter versions: %d", versions)
	}

	time.Sleep(2100 * time.Millisecond)
	invalid := strings.Replace(body, "7f9619ff", "8f9619ff", 1)
	invalid = strings.Replace(invalid, `"maxHR":188`, `"maxHR":52`, 1)
	code, _ = postJSONHTTP(t, handler, f.enrollment.Token, "/v2/model-inputs/trimp-parameters", []byte(invalid))
	if code != 422 {
		t.Fatal("invalid trimp status", code)
	}
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM analysis_parameter_sets WHERE dataset_id=$1`, f.p.DatasetID).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 1 {
		t.Fatalf("invalid input appended parameter versions: %d", versions)
	}

	code, _ = postJSONHTTP(t, handler, f.enrollment.Token, "/v2/model-inputs/"+id+"/revocations", []byte(`{"reason":"correction"}`))
	if code != 422 {
		t.Fatal("trimp revocation must be rejected", code)
	}

	var parameterPayload string
	if err := f.s.Pool.QueryRow(ctx, `SELECT payload::text FROM athlete_model_inputs WHERE dataset_id=$1 AND id=$2`, f.p.DatasetID, id).Scan(&parameterPayload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(parameterPayload, `"restingHR": 52`) && !strings.Contains(parameterPayload, `"restingHR":52`) {
		t.Fatalf("stored trimp payload=%s", parameterPayload)
	}
}
