package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rai/server/internal/syncing"
)

type API struct {
	store    syncing.Store
	log      *slog.Logger
	uploads  chan struct{}
	uploadMu sync.Mutex
	devices  map[uploadDevice]bool
}

type uploadDevice struct{ datasetID, deviceID string }

func New(store syncing.Store, log *slog.Logger) http.Handler {
	a := &API{store: store, log: log, uploads: make(chan struct{}, 2), devices: make(map[uploadDevice]bool)}
	m := http.NewServeMux()
	live := func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]string{"status": "alive"}) }
	ready := func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := store.Ready(ctx); err != nil {
			log.Warn("readiness_failed", "reason", "database_or_schema_unavailable")
			respond(w, 503, map[string]string{"code": "not_ready"})
			return
		}
		respond(w, 200, map[string]string{"status": "ready"})
	}
	m.HandleFunc("GET /livez", live)
	m.HandleFunc("GET /health", live)
	m.HandleFunc("GET /readyz", ready)
	m.HandleFunc("GET /ready", ready)
	m.Handle("GET /v2/handshake", a.auth(syncing.ReadLane, a.handshake))
	m.Handle("GET /v2/sync/changes", a.auth(syncing.ReadLane, a.changes))
	m.Handle("POST /v2/sync/streams", a.auth(syncing.WriteLane, a.registerStream))
	m.Handle("GET /v2/sync/streams/{id}", a.auth(syncing.ReadLane, a.stream))
	m.Handle("POST /v2/sync/batches", a.auth(syncing.WriteLane, a.registerBatch))
	m.Handle("GET /v2/sync/batches/{id}", a.auth(syncing.ReadLane, a.batch))
	m.Handle("GET /v2/sync/pages/{id}", a.auth(syncing.ReadLane, a.page))
	m.Handle("PUT /v2/sync/batches/{id}/body", a.auth(syncing.UploadLane, a.body))
	m.Handle("POST /v2/sync/series", a.auth(syncing.WriteLane, a.registerSeries))
	m.Handle("GET /v2/sync/series/{id}", a.auth(syncing.ReadLane, a.series))
	m.Handle("PUT /v2/sync/series/{id}/chunks/{index}", a.auth(syncing.UploadLane, a.seriesChunk))
	m.Handle("POST /v2/sync/series/{id}/seal", a.auth(syncing.WriteLane, a.sealSeries))
	m.Handle("POST /v2/sync/workout-packages", a.auth(syncing.WriteLane, a.registerWorkoutPackage))
	m.Handle("GET /v2/sync/workout-packages/{id}", a.auth(syncing.ReadLane, a.workoutPackage))
	m.Handle("PUT /v2/sync/workout-packages/{id}/chunks/{index}", a.auth(syncing.UploadLane, a.workoutPackageChunk))
	m.Handle("POST /v2/sync/workout-packages/{id}/seal", a.auth(syncing.WriteLane, a.sealWorkoutPackage))
	m.Handle("POST /v2/sync/associations", a.auth(syncing.WriteLane, a.associateSample))
	m.Handle("POST /v2/sync/workout-enrichments", a.auth(syncing.WriteLane, a.enrichWorkout))
	m.Handle("GET /v2/sync/workout-enrichments/{id}", a.auth(syncing.ReadLane, a.workoutEnrichment))
	m.Handle("POST /v2/routes", a.auth(syncing.WriteLane, a.registerRoute))
	m.Handle("GET /v2/routes/{id}", a.auth(syncing.ReadLane, a.route))
	m.Handle("PUT /v2/routes/{id}/chunks/{index}", a.auth(syncing.UploadLane, a.routeChunk))
	m.Handle("POST /v2/routes/{id}/seal", a.auth(syncing.WriteLane, a.sealRoute))
	m.Handle("POST /v2/routes/{id}/delete", a.auth(syncing.WriteLane, a.deleteRoute))
	m.Handle("GET /v2/analysis/workouts/{id}", a.auth(syncing.ReadLane, a.workoutAnalysis))
	m.Handle("GET /v2/workouts", a.auth(syncing.ReadLane, a.product))
	m.Handle("GET /v2/workouts/{id}", a.auth(syncing.ReadLane, a.product))
	m.Handle("GET /v2/product/overview", a.auth(syncing.ReadLane, a.product))
	m.Handle("GET /v2/analysis/history", a.auth(syncing.ReadLane, a.product))
	m.Handle("GET /v2/analysis/state", a.auth(syncing.ReadLane, a.trainingState))
	m.Handle("GET /v2/training/plans/current", a.auth(syncing.ReadLane, a.training))
	m.Handle("GET /v2/training/plans/{id}", a.auth(syncing.ReadLane, a.training))
	m.Handle("GET /v2/training/plans/{id}/effective", a.auth(syncing.ReadLane, a.training))
	m.Handle("GET /v2/training/plans/{id}/effective/{version}", a.auth(syncing.ReadLane, a.training))
	m.Handle("GET /v2/training/plans/{id}/history", a.auth(syncing.ReadLane, a.training))
	m.Handle("GET /v2/training/executions/{id}", a.auth(syncing.ReadLane, a.training))
	m.Handle("POST /v2/training/plans", a.auth(syncing.WriteLane, a.training))
	m.Handle("POST /v2/training/plans/{id}/changes", a.auth(syncing.WriteLane, a.training))
	m.Handle("POST /v2/training/plans/{id}/overrides", a.auth(syncing.WriteLane, a.training))
	m.Handle("POST /v2/training/executions", a.auth(syncing.WriteLane, a.training))
	m.Handle("POST /v2/training/executions/{id}/changes", a.auth(syncing.WriteLane, a.training))
	m.Handle("GET /v2/jobs/{id}", a.auth(syncing.ReadLane, a.job))
	return a.observe(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		m.ServeHTTP(w, r)
	}))
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// observe emits correlation-only request metrics. It intentionally records no
// request body, health values, token or database diagnostics.
func (a *API) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		requestID := r.Header.Get("X-Request-ID")
		if !syncing.ValidID(requestID) {
			requestID = syncing.NewID()
		}
		w.Header().Set("X-Request-ID", requestID)
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		a.log.Info("http_request", "request_id", requestID, "method", r.Method, "path", r.URL.Path,
			"status", sw.status, "duration_ms", time.Since(start).Milliseconds())
	})
}

type handler func(http.ResponseWriter, *http.Request, syncing.Principal) error

func (a *API) changes(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	q := r.URL.Query()
	for k, values := range q {
		if (k != "after" && k != "limit") || len(values) != 1 {
			return syncing.Fail(422, "invalid_change_query")
		}
	}
	after := q.Get("after")
	if !q.Has("after") {
		after = "0"
	} else if after == "" {
		return syncing.Fail(422, "invalid_change_cursor")
	}
	limit := 50
	if q.Has("limit") {
		v := q.Get("limit")
		var err error
		limit, err = strconv.Atoi(v)
		if err != nil || strconv.Itoa(limit) != v || limit < 1 || limit > syncing.MaxChangePage {
			return syncing.Fail(422, "invalid_change_limit")
		}
	}
	page, err := a.store.Changes(r.Context(), p, after, limit)
	if err != nil {
		return err
	}
	respond(w, 200, page)
	return nil
}

func (a *API) auth(lane syncing.AdmissionLane, next handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") || len(header) < 50 || len(header) > 256 {
			a.fail(w, syncing.Fail(401, "unauthorized"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		p, err := a.store.Authenticate(ctx, strings.TrimPrefix(header, "Bearer "))
		if err == nil && lane != syncing.ReadLane && !p.CanCollect {
			err = syncing.Fail(403, "read_only_device")
		}
		if err == nil {
			err = a.store.Admit(ctx, p, lane)
		}
		if err == nil {
			err = next(w, r, p)
		}
		if err != nil {
			a.fail(w, err)
		}
	})
}
func (a *API) fail(w http.ResponseWriter, err error) {
	var e *syncing.Error
	if errors.As(err, &e) {
		if e.Status == 401 {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		if e.Status == 429 {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, e.RetryAfterSeconds)))
		}
		respond(w, e.Status, e)
		return
	}
	// Database errors can contain sample values or credential-bearing URLs.
	a.log.Error("request_failed", "reason", "storage_unavailable")
	w.Header().Set("Retry-After", "2")
	respond(w, 503, map[string]string{"code": "temporarily_unavailable"})
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func readJSON(r *http.Request, v any) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return syncing.Fail(415, "unsupported_media_type")
	}
	if r.Header.Get("Content-Encoding") != "" {
		return syncing.Fail(415, "unsupported_content_encoding")
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 16385))
	if err != nil {
		return syncing.Fail(400, "body_read_failed")
	}
	if len(b) > 16384 {
		return syncing.Fail(413, "batch_too_large")
	}
	return syncing.DecodeJSON(b, v)
}
func pathID(r *http.Request) (string, error) {
	id := r.PathValue("id")
	if !syncing.ValidID(id) {
		return "", syncing.Fail(422, "unsupported_payload")
	}
	return id, nil
}
func (a *API) handshake(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	m, err := a.store.Metadata(r.Context(), p)
	if err != nil {
		return err
	}
	types := make([]string, 0, len(syncing.Units))
	for k := range syncing.Units {
		types = append(types, k)
	}
	sort.Strings(types)
	respond(w, 200, map[string]any{"protocolVersion": 2, "datasetID": m.DatasetID, "datasetEpoch": m.DatasetEpoch, "serviceID": m.ServiceID, "timezone": m.Timezone, "commitRevision": m.CommitRevision,
		"limits":       map[string]any{"wireBytes": syncing.MaxWireBytes, "decodedBytes": syncing.MaxDecodedBytes, "events": syncing.MaxEvents, "lineBytes": syncing.MaxLineBytes, "workoutStatistics": syncing.MaxWorkoutStatistics, "workoutEvents": syncing.MaxWorkoutEvents, "workoutDetails": syncing.MaxWorkoutDetails, "workoutActivities": syncing.MaxWorkoutActivities, "workoutPackageChunks": syncing.MaxWorkoutPackageChunks, "workoutPackageDetails": syncing.MaxWorkoutPackageDetails, "workoutPackageDetailsPerChunk": syncing.MaxWorkoutPackageDetailsPerChunk, "pageFragments": syncing.MaxPageFragments, "seriesChunks": syncing.MaxSeriesChunks, "seriesPoints": syncing.MaxSeriesPoints, "seriesPointsPerChunk": syncing.MaxSeriesPointsPerChunk, "routeChunks": syncing.MaxRouteChunks, "routePoints": syncing.MaxRoutePoints, "routePointsPerChunk": syncing.MaxRoutePointsPerChunk, "requests": syncing.RequestBudgets(), "processUploads": 2, "processDeviceUploads": 1, "datasetUploads": syncing.DatasetUploads, "deviceUploads": 1, "uploadLeaseSeconds": syncing.UploadLeaseSeconds},
		"capabilities": map[string]any{"sampleTypes": types, "contentEncoding": "gzip", "workouts": true, "workoutType": syncing.WorkoutType, "workoutPayloadVersion": 2, "pageReceipts": true, "workoutActivities": true, "workoutPackages": true, "quantitySeries": true, "routes": true, "sampleWorkoutAssociations": true, "workoutEnrichment": true, "changefeed": false, "analysis": true}})
	return nil
}
func (a *API) registerStream(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	var req syncing.StreamRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	result, err := a.store.RegisterStream(r.Context(), p, req)
	if err == nil {
		respond(w, 200, result)
	}
	return err
}
func (a *API) stream(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	result, err := a.store.Stream(r.Context(), p, id)
	if err == nil {
		respond(w, 200, result)
	}
	return err
}
func (a *API) registerBatch(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	var req syncing.Envelope
	if err := readJSON(r, &req); err != nil {
		return err
	}
	result, err := a.store.RegisterBatch(r.Context(), p, req)
	if err == nil {
		respond(w, 200, result)
	}
	return err
}
func (a *API) batch(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	result, err := a.store.Batch(r.Context(), p, id)
	if err == nil {
		respond(w, 200, result)
	}
	return err
}
func (a *API) page(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	result, err := a.store.Page(r.Context(), p, id)
	if err == nil {
		respond(w, 200, result)
	}
	return err
}
func (a *API) registerSeries(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	var req syncing.SeriesRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	result, err := a.store.RegisterSeries(r.Context(), p, req)
	if err == nil {
		respond(w, 200, result)
	}
	return err
}
func (a *API) series(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	result, err := a.store.Series(r.Context(), p, id)
	if err == nil {
		respond(w, 200, result)
	}
	return err
}
func (a *API) sealSeries(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	var req syncing.SeriesSeal
	if err = readJSON(r, &req); err != nil {
		return err
	}
	result, err := a.store.SealSeries(r.Context(), p, id, req)
	if err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		// A committed seal is recoverable through GET even if this write fails.
		_, _ = w.Write(result)
	}
	return err
}
func (a *API) seriesChunk(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	if !p.CanCollect {
		return syncing.Fail(403, "read_only_device")
	}
	id, err := pathID(r)
	if err != nil {
		return err
	}
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || index < 0 || index >= syncing.MaxSeriesChunks {
		return syncing.Fail(422, "unsupported_payload")
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/x-rai-health-series+ndjson" {
		return syncing.Fail(415, "unsupported_media_type")
	}
	if r.Header.Get("Content-Encoding") != "gzip" {
		return syncing.Fail(415, "unsupported_content_encoding")
	}
	release, err := a.acquireUpload(p)
	if err != nil {
		return err
	}
	defer release()
	lease, err := a.store.AcquireUpload(r.Context(), p)
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := a.store.ReleaseUpload(ctx, p, lease); err != nil {
			a.log.Error("upload_release_failed", "reason", "storage_unavailable")
		}
	}()
	stopClose := context.AfterFunc(r.Context(), func() { _ = r.Body.Close() })
	defer stopClose()
	state, err := a.store.Series(r.Context(), p, id)
	if err != nil {
		return err
	}
	chunk, err := syncing.ParseSeriesChunk(r.Body, state.SampleType, state.SampleStartUs, state.SampleEndUs)
	if err != nil {
		return err
	}
	chunk.SeriesID = id
	chunk.ChunkIndex = index
	if err = r.Context().Err(); err != nil {
		return err
	}
	receipt, err := a.store.CommitSeriesChunk(r.Context(), p, chunk, lease)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	// A response write failure cannot undo the committed chunk; GET series
	// recovers committedChunks and the original chunk receipt is idempotent.
	_, _ = w.Write(receipt)
	return nil
}
func (a *API) registerWorkoutPackage(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	var req syncing.WorkoutPackageRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	result, err := a.store.RegisterWorkoutPackage(r.Context(), p, req)
	if err == nil {
		respond(w, 200, result)
	}
	return err
}
func (a *API) workoutPackage(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	result, err := a.store.WorkoutPackage(r.Context(), p, id)
	if err == nil {
		respond(w, 200, result)
	}
	return err
}
func (a *API) sealWorkoutPackage(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	var req syncing.WorkoutPackageSeal
	if err = readJSON(r, &req); err != nil {
		return err
	}
	result, err := a.store.SealWorkoutPackage(r.Context(), p, id, req)
	if err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(result)
	}
	return err
}
func (a *API) workoutPackageChunk(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	if !p.CanCollect {
		return syncing.Fail(403, "read_only_device")
	}
	id, err := pathID(r)
	if err != nil {
		return err
	}
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || index < 0 || index >= syncing.MaxWorkoutPackageChunks {
		return syncing.Fail(422, "unsupported_payload")
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/x-rai-health-workout-detail+ndjson" {
		return syncing.Fail(415, "unsupported_media_type")
	}
	if r.Header.Get("Content-Encoding") != "gzip" {
		return syncing.Fail(415, "unsupported_content_encoding")
	}
	release, err := a.acquireUpload(p)
	if err != nil {
		return err
	}
	defer release()
	lease, err := a.store.AcquireUpload(r.Context(), p)
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := a.store.ReleaseUpload(ctx, p, lease); err != nil {
			a.log.Error("upload_release_failed", "reason", "storage_unavailable")
		}
	}()
	stopClose := context.AfterFunc(r.Context(), func() { _ = r.Body.Close() })
	defer stopClose()
	state, err := a.store.WorkoutPackage(r.Context(), p, id)
	if err != nil {
		return err
	}
	chunk, err := syncing.ParseWorkoutDetailChunk(r.Body, state.StartUs, state.EndUs)
	if err != nil {
		return err
	}
	chunk.WorkoutID = id
	chunk.ChunkIndex = index
	if err = r.Context().Err(); err != nil {
		return err
	}
	receipt, err := a.store.CommitWorkoutPackageChunk(r.Context(), p, chunk, lease)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(receipt)
	return nil
}
func (a *API) registerRoute(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	var req syncing.RouteManifest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	result, err := a.store.RegisterRoute(r.Context(), p, req)
	if err == nil {
		respond(w, 200, result)
	}
	return err
}
func (a *API) route(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	result, err := a.store.Route(r.Context(), p, id)
	if err == nil {
		respond(w, 200, result)
	}
	return err
}
func (a *API) routeChunk(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	if !p.CanCollect {
		return syncing.Fail(403, "read_only_device")
	}
	id, err := pathID(r)
	if err != nil {
		return err
	}
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || index < 0 || index >= syncing.MaxRouteChunks {
		return syncing.Fail(422, "unsupported_payload")
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/x-rai-health-route+ndjson" {
		return syncing.Fail(415, "unsupported_media_type")
	}
	if r.Header.Get("Content-Encoding") != "gzip" {
		return syncing.Fail(415, "unsupported_content_encoding")
	}
	release, err := a.acquireUpload(p)
	if err != nil {
		return err
	}
	defer release()
	lease, err := a.store.AcquireUpload(r.Context(), p)
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := a.store.ReleaseUpload(ctx, p, lease); err != nil {
			a.log.Error("upload_release_failed", "reason", "storage_unavailable")
		}
	}()
	stopClose := context.AfterFunc(r.Context(), func() { _ = r.Body.Close() })
	defer stopClose()
	chunk, err := syncing.ParseRouteChunk(r.Body)
	if err != nil {
		return err
	}
	chunk.RouteID, chunk.ChunkIndex = id, index
	if err = r.Context().Err(); err != nil {
		return err
	}
	receipt, err := a.store.CommitRouteChunk(r.Context(), p, chunk, lease)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(receipt)
	return nil
}
func (a *API) sealRoute(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	var req syncing.RouteSeal
	if err = readJSON(r, &req); err != nil {
		return err
	}
	result, err := a.store.SealRoute(r.Context(), p, id, req)
	if err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(result)
	}
	return err
}
func (a *API) deleteRoute(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	var req syncing.RouteDeleteRequest
	if err = readJSON(r, &req); err != nil {
		return err
	}
	result, err := a.store.DeleteRoute(r.Context(), p, id, req)
	if err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(result)
	}
	return err
}
func (a *API) associateSample(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	var req syncing.AssociationRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	result, err := a.store.AssociateSample(r.Context(), p, req)
	if err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(result)
	}
	return err
}
func (a *API) enrichWorkout(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	var req syncing.WorkoutEnrichmentRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	result, err := a.store.EnrichWorkout(r.Context(), p, req)
	if err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(result)
	}
	return err
}
func (a *API) workoutEnrichment(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	result, err := a.store.WorkoutEnrichment(r.Context(), p, id)
	if err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(result)
	}
	return err
}
func (a *API) job(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	result, err := a.store.Job(r.Context(), p, id)
	if err == nil {
		respond(w, 200, result)
	}
	return err
}
func (a *API) body(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	if !p.CanCollect {
		return syncing.Fail(403, "read_only_device")
	}
	id, err := pathID(r)
	if err != nil {
		return err
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/x-rai-health-delta+ndjson" {
		return syncing.Fail(415, "unsupported_media_type")
	}
	if r.Header.Get("Content-Encoding") != "gzip" {
		return syncing.Fail(415, "unsupported_content_encoding")
	}
	release, err := a.acquireUpload(p)
	if err != nil {
		return err
	}
	defer release()
	lease, err := a.store.AcquireUpload(r.Context(), p)
	if err != nil {
		return err
	}
	defer func() {
		// Request cancellation must not prevent best-effort cleanup. On database
		// failure the fixed TTL recovers the slot; never log its token/identity.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := a.store.ReleaseUpload(ctx, p, lease); err != nil {
			a.log.Error("upload_release_failed", "reason", "storage_unavailable")
		}
	}()
	// Closing a network body interrupts blocked reads when the request expires.
	// No detached parsing goroutine can outlive the process-local slot.
	stopClose := context.AfterFunc(r.Context(), func() { _ = r.Body.Close() })
	defer stopClose()
	b, err := a.store.Batch(r.Context(), p, id)
	if err != nil {
		return err
	}
	stream, err := a.store.Stream(r.Context(), p, b.Envelope.StreamID)
	if err != nil {
		return err
	}
	events, err := syncing.ParseBody(r.Body, b.Envelope, stream.SampleType)
	if err != nil {
		return err
	}
	if err = r.Context().Err(); err != nil {
		return err
	}
	started := time.Now()
	receipt, err := a.store.CommitUpload(r.Context(), p, b.Envelope, events, lease)
	if err != nil {
		return err
	}
	var committed syncing.Receipt
	_ = json.Unmarshal(receipt, &committed)
	a.log.Info("sync_batch_committed", "batch_id", b.Envelope.BatchID, "event_count", len(events),
		"inserted", committed.Counts.Inserted, "unchanged", committed.Counts.Unchanged,
		"deleted", committed.Counts.Deleted, "tombstoned", committed.Counts.Tombstoned,
		"suppressed", committed.Counts.Suppressed, "commit_ms", time.Since(started).Milliseconds())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	// A response write failure cannot undo the committed transaction. The client
	// recovers using GET batchID; do not relabel it as a storage failure.
	_, _ = w.Write(receipt)
	return nil
}

// Only active uploads occupy map entries. The global two-slot cap bounds both
// memory and concurrent parsing; device keys never depend on bearer tokens.
func (a *API) acquireUpload(p syncing.Principal) (func(), error) {
	a.uploadMu.Lock()
	defer a.uploadMu.Unlock()
	key := uploadDevice{p.DatasetID, p.DeviceID}
	if a.devices[key] {
		return nil, &syncing.Error{Status: 429, Code: "device_upload_capacity_exceeded", RetryAfterSeconds: 1}
	}
	select {
	case a.uploads <- struct{}{}:
		a.devices[key] = true
		return func() {
			a.uploadMu.Lock()
			defer a.uploadMu.Unlock()
			delete(a.devices, key)
			<-a.uploads
		}, nil
	default:
		return nil, &syncing.Error{Status: 429, Code: "upload_capacity_exceeded", RetryAfterSeconds: 1}
	}
}
