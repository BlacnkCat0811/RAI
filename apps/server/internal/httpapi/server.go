package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	statusSlots   chan struct{}
	statusMu      sync.Mutex
	statusBuckets map[uploadDevice]statusBudget
}

type uploadDevice struct{ datasetID, deviceID string }

type statusBudget struct {
	tokens  float64
	updated time.Time
}

// ProcessUploadSlots is the process-wide concurrent upload ceiling. It is the
// default budget the handshake advertises and the capacity of the upload slot
// channel; performance entries must not raise it.
const ProcessUploadSlots = 2

// Status queries never write. They still hold a bounded process-local slot so
// one client cannot occupy every database connection with polling.
const (
	ProcessStatusSlots     = 8
	statusQueriesPerMinute = 60
	statusBurst            = 10
	maxStatusBuckets       = 1024
)

func New(store syncing.Store, log *slog.Logger) http.Handler {
	a := &API{store: store, log: log, uploads: make(chan struct{}, ProcessUploadSlots), devices: make(map[uploadDevice]bool),
		statusSlots: make(chan struct{}, ProcessStatusSlots), statusBuckets: make(map[uploadDevice]statusBudget)}
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
	m.Handle("POST /v2/sync/vo2max-facts", a.auth(syncing.UploadLane, a.vo2Upload))
	m.Handle("GET /v2/sync/vo2max-facts/coverage", a.auth(syncing.ReadLane, a.vo2Coverage))
	m.Handle("POST /v2/sync/daily-facts", a.auth(syncing.UploadLane, a.dailyUpload))
	m.Handle("GET /v2/sync/daily-facts/coverage", a.auth(syncing.ReadLane, a.dailyCoverage))
	// UUID ingest skips Admit: its request budget has a foreign key into
	// datasets, and acceptance must proceed while a fact import holds that row.
	m.Handle("POST /v2/sync/workout-ingests/{workoutID}", a.ingestAuth(syncing.UploadLane, a.acceptWorkoutIngest))
	m.Handle("GET /v2/sync/workout-ingests/{workoutID}", a.ingestAuth(syncing.ReadLane, a.workoutIngestStatus))
	m.Handle("POST /v2/sync/workout-ingests/status", a.ingestAuth(syncing.ReadLane, a.workoutIngestStatuses))
	m.Handle("POST /v2/model-inputs/{kind}", a.auth(syncing.UploadLane, a.modelInput))
	m.Handle("POST /v2/model-inputs/{id}/revocations", a.auth(syncing.UploadLane, a.modelInputRevocation))
	m.Handle("GET /v2/analysis/workouts/{id}", a.auth(syncing.ReadLane, a.workoutAnalysis))
	m.Handle("GET /v2/analysis/parameters", a.auth(syncing.ReadLane, a.analysisParameters))
	m.Handle("GET /v2/workouts", a.auth(syncing.ReadLane, a.product))
	m.Handle("GET /v2/workouts/{id}", a.auth(syncing.ReadLane, a.product))
	m.Handle("GET /v2/workouts/{id}/detail", a.auth(syncing.ReadLane, a.workoutDetail))
	m.Handle("GET /v2/workouts/{id}/series", a.auth(syncing.ReadLane, a.workoutSeries))
	m.Handle("GET /v2/workouts/{id}/freshness", a.auth(syncing.ReadLane, a.workoutFreshness))
	m.Handle("GET /v2/product/overview", a.auth(syncing.ReadLane, a.product))
	m.Handle("GET /v2/analysis/athlete", a.auth(syncing.ReadLane, a.athleteProduct))
	m.Handle("GET /v2/analysis/athlete/history", a.auth(syncing.ReadLane, a.athleteHistory))
	m.Handle("GET /v2/analysis/athlete/diagnostics", a.auth(syncing.ReadLane, a.athleteDiagnostics))
	m.Handle("GET /v2/analysis/history", a.auth(syncing.ReadLane, a.product))
	m.Handle("GET /v2/analysis/state", a.auth(syncing.ReadLane, a.trainingState))
	m.Handle("GET /v2/training/plans/current", a.auth(syncing.ReadLane, a.training))
	m.Handle("GET /v2/training/plans/{id}", a.auth(syncing.ReadLane, a.training))
	m.Handle("GET /v2/training/plans/{id}/effective", a.auth(syncing.ReadLane, a.training))
	m.Handle("GET /v2/training/plans/{id}/effective/{version}", a.auth(syncing.ReadLane, a.training))
	m.Handle("GET /v2/training/plans/{id}/effective/{version}/audit", a.auth(syncing.ReadLane, a.training))
	m.Handle("GET /v2/training/plans/{id}/effective/{version}/detail", a.auth(syncing.ReadLane, a.training))
	m.Handle("GET /v2/training/plans/{id}/history", a.auth(syncing.ReadLane, a.training))
	m.Handle("GET /v2/training/plans/{id}/history/versions", a.auth(syncing.ReadLane, a.training))
	m.Handle("GET /v2/training/executions/{id}", a.auth(syncing.ReadLane, a.training))
	m.Handle("POST /v2/training/plans", a.auth(syncing.WriteLane, a.training))
	m.Handle("POST /v2/training/plans/{id}/changes", a.auth(syncing.WriteLane, a.training))
	m.Handle("POST /v2/training/plans/{id}/overrides", a.auth(syncing.WriteLane, a.training))
	m.Handle("POST /v2/training/executions", a.auth(syncing.WriteLane, a.training))
	m.Handle("POST /v2/training/executions/{id}/changes", a.auth(syncing.WriteLane, a.training))
	m.Handle("GET /v2/jobs/{id}", a.auth(syncing.ReadLane, a.job))
	// Removed write protocols remain unavailable. A valid retired identity still
	// receives lifecycle semantics instead of hiding the existing dataset as 404.
	m.Handle("/v2/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			header := r.Header.Get("Authorization")
			if strings.HasPrefix(header, "Bearer ") && len(header) >= 50 && len(header) <= 256 {
				ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
				defer cancel()
				p, err := a.store.Authenticate(ctx, strings.TrimPrefix(header, "Bearer "))
				if err == nil && p.DatasetRetired {
					a.fail(w, syncing.Fail(409, "dataset_retired"))
					return
				}
			}
		}
		http.NotFound(w, r)
	}))
	return a.observe(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		m.ServeHTTP(w, r)
	}))
}

type statusWriter struct {
	http.ResponseWriter
	status    int
	errorCode string
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *statusWriter) setErrorCode(code string)    { w.errorCode = code }

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
		operation := r.Pattern
		if operation == "" {
			operation = "unmatched"
		}
		a.log.Info("http_request", "request_hash", logIdentity(requestID), "component", "api", "operation", operation,
			"status", sw.status, "error_code", sw.errorCode, "duration_ms", time.Since(start).Milliseconds())
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
	return a.authenticate(lane, true, next)
}

// ingestAuth keeps bearer, lifecycle, role and transport validation but skips
// the database request-budget write. The UUID endpoints enforce their own
// process-local limits and the ingest backlog quota.
func (a *API) ingestAuth(lane syncing.AdmissionLane, next handler) http.Handler {
	return a.authenticate(lane, false, next)
}

func (a *API) authenticate(lane syncing.AdmissionLane, admit bool, next handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") || len(header) < 50 || len(header) > 256 {
			a.fail(w, syncing.Fail(401, "unauthorized"))
			return
		}
		timeout := 20 * time.Second
		if lane == syncing.UploadLane {
			// Body upload, finite processing, then ACK have separate budgets.
			timeout = BundleHandlerBudget
		}
		readBudget, writeBudget := timeout, timeout+ACKWriteBudget
		if lane == syncing.UploadLane {
			readBudget = BundleBodyBudget
		}
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(time.Now().Add(readBudget))
		_ = controller.SetWriteDeadline(time.Now().Add(writeBudget))
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		r = r.WithContext(ctx)
		p, err := a.store.Authenticate(ctx, strings.TrimPrefix(header, "Bearer "))
		if err == nil && p.DatasetRetired && r.Method != http.MethodGet && r.Method != http.MethodHead {
			err = syncing.Fail(409, "dataset_retired")
		}
		if err == nil && lane != syncing.ReadLane && !p.CanCollect {
			err = syncing.Fail(403, "read_only_device")
		}
		if err == nil && admit {
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
	if errors.Is(err, context.DeadlineExceeded) {
		err = syncing.Fail(503, "request_timeout")
	}
	if errors.Is(err, context.Canceled) {
		err = syncing.Fail(408, "request_cancelled")
	}

	var sqlFailure interface{ SQLState() string }
	if errors.As(err, &sqlFailure) {
		switch sqlFailure.SQLState() {
		case "57014":
			err = syncing.Fail(503, "database_statement_timeout_or_cancel")
		case "25P04":
			err = syncing.Fail(503, "database_transaction_timeout")
		}
	}
	var e *syncing.Error
	if errors.As(err, &e) {
		if observer, ok := w.(interface{ setErrorCode(string) }); ok {
			observer.setErrorCode(e.Code)
		}
		if e.Status == 401 {
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		if e.Status >= 500 {
			w.Header().Set("Retry-After", "2")
		}
		if e.Status == 429 {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, e.RetryAfterSeconds)))
		}
		respond(w, e.Status, e)
		return
	}
	// Database errors can contain sample values or credential-bearing URLs.
	a.log.Error("request_failed", "reason", "storage_unavailable")
	if observer, ok := w.(interface{ setErrorCode(string) }); ok {
		observer.setErrorCode("temporarily_unavailable")
	}
	w.Header().Set("Retry-After", "2")
	respond(w, 503, map[string]string{"code": "temporarily_unavailable"})
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Share the HTTP wire boundary while retaining each DTO's strict decoder and
// endpoint-specific budget. Decode errors must not disguise media/size errors.
func readJSONBytes(r *http.Request, limit int64, tooLargeCode string) ([]byte, error) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return nil, syncing.Fail(415, "unsupported_media_type")
	}
	if r.Header.Get("Content-Encoding") != "" {
		return nil, syncing.Fail(415, "unsupported_content_encoding")
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, syncing.Fail(400, "body_read_failed")
	}
	if int64(len(b)) > limit {
		return nil, syncing.Fail(413, tooLargeCode)
	}
	return b, nil
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
	// Protocol 6 is the only raw Workout payload. The session protocol itself
	// stays at version 3; it versions the handshake shape, not the payload.
	respond(w, 200, map[string]any{"protocolVersion": 3, "deviceID": m.DeviceID, "collectorEpoch": m.CollectorEpoch, "datasetStatus": m.DatasetStatus, "datasetID": m.DatasetID, "datasetEpoch": m.DatasetEpoch, "serviceID": m.ServiceID, "timezone": m.Timezone, "commitRevision": m.CommitRevision,
		"limits":       map[string]any{"bundleBytes": syncing.MaxWorkoutBundleBytes, "bundleWireBytes": MaxBundleWireBytes, "bundleCompressedBytes": MaxBundleCompressedBytes, "bundleEvents": syncing.MaxWorkoutBundleEvents, "bundleRoutes": syncing.MaxBundleRoutes, "bundleRoutePoints": syncing.MaxBundleRoutePoints, "requests": syncing.RequestBudgets(), "processUploads": ProcessUploadSlots, "processDeviceUploads": 1, "datasetUploads": syncing.DatasetUploads, "deviceUploads": 1},
		"capabilities": map[string]any{"workoutSourcePolicyVersion": syncing.WorkoutSourcePolicyVersion, "workoutUUIDIngestVersion": 1, "workoutUUIDIngestAccepting": true, "vo2maxFactsVersion": 1, "vo2maxMetadataRegistryVersion": 1, "vo2maxMaxChanges": syncing.MaxVO2Changes, "vo2maxMaxBytes": syncing.MaxVO2Bytes, "dailyFactsVersion": 1, "dailyFactTypes": syncing.DailyTypes, "dailyMaxChanges": syncing.MaxDailyChanges, "dailyMaxBytes": syncing.MaxDailyBytes, "sampleTypes": types, "workouts": true, "workoutType": syncing.WorkoutType, "workoutPayloadVersion": 6, "bundleGzipPart": true, "routes": true, "sampleWorkoutAssociations": true, "changefeed": false, "analysis": true}})
	return nil
}

// acceptWorkoutIngest stores the first raw protocol-6 snapshot for this UUID.
// The receipt is returned only after the acceptance transaction commits.
func (a *API) acceptWorkoutIngest(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	workoutID := r.PathValue("workoutID")
	if !syncing.ValidID(workoutID) {
		return syncing.Fail(422, "unsupported_payload")
	}
	release, err := a.acquireUpload(p)
	if err != nil {
		return err
	}
	defer release()
	raw, read, err := readBundleBody(w, r)
	if err != nil {
		return err
	}
	envelope, err := syncing.DecodeWorkoutIngestEnvelope(raw)
	if err != nil {
		return err
	}
	if envelope.WorkoutID != workoutID {
		return syncing.Fail(422, "workout_ingest_mismatch")
	}
	receipt, err := a.store.AcceptWorkoutIngest(r.Context(), p, envelope, raw)
	if err != nil {
		return err
	}
	a.log.Info("workout_ingest_accepted", "component", "sync", "operation", "workout_ingest_accept",
		"request_hash", logIdentity(w.Header().Get("X-Request-ID")), "workout_hash", logIdentity(workoutID),
		"decoded_bytes", read.DecodedBytes)
	respond(w, 200, json.RawMessage(receipt))
	return nil
}

func (a *API) workoutIngestStatus(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	workoutID := r.PathValue("workoutID")
	if !syncing.ValidID(workoutID) {
		return syncing.Fail(422, "unsupported_payload")
	}
	release, err := a.acquireStatusQuery(p)
	if err != nil {
		return err
	}
	defer release()
	status, err := a.store.WorkoutIngest(r.Context(), p, workoutID)
	if err != nil {
		return err
	}
	respond(w, 200, status)
	return nil
}

// workoutIngestStatuses answers up to MaxIngestStatusBatch UUIDs in one read so
// the app can render its whole Workout list (and recover after a reinstall)
// without one request per record.
func (a *API) workoutIngestStatuses(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	var body struct {
		WorkoutIDs []string `json:"workoutIDs"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		return syncing.Fail(422, "unsupported_payload")
	}
	if len(body.WorkoutIDs) == 0 || len(body.WorkoutIDs) > syncing.MaxIngestStatusBatch {
		return syncing.Fail(422, "unsupported_payload")
	}
	for _, id := range body.WorkoutIDs {
		if !syncing.ValidID(id) {
			return syncing.Fail(422, "unsupported_payload")
		}
	}
	release, err := a.acquireStatusQuery(p)
	if err != nil {
		return err
	}
	defer release()
	// Optional capability: kept off syncing.Store so existing fakes still compile.
	batcher, ok := a.store.(interface {
		WorkoutIngestStatuses(context.Context, syncing.Principal, []string) ([]syncing.WorkoutIngestStatus, error)
	})
	if !ok {
		return syncing.Fail(501, "not_implemented")
	}
	items, err := batcher.WorkoutIngestStatuses(r.Context(), p, body.WorkoutIDs)
	if err != nil {
		return err
	}
	respond(w, 200, map[string]any{"items": items})
	return nil
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

// acquireStatusQuery bounds one indexed status read per slot and applies a
// small per-device token bucket. The map is pruned before it can grow without
// bound; devices never key on bearer tokens.
func (a *API) acquireStatusQuery(p syncing.Principal) (func(), error) {
	now := time.Now()
	key := uploadDevice{p.DatasetID, p.DeviceID}
	a.statusMu.Lock()
	bucket, ok := a.statusBuckets[key]
	if !ok {
		if len(a.statusBuckets) >= maxStatusBuckets {
			for stale, value := range a.statusBuckets {
				if now.Sub(value.updated) > 2*time.Minute {
					delete(a.statusBuckets, stale)
				}
			}
			if len(a.statusBuckets) >= maxStatusBuckets {
				a.statusMu.Unlock()
				return nil, &syncing.Error{Status: 429, Code: "status_capacity_exceeded", RetryAfterSeconds: 1}
			}
		}
		bucket = statusBudget{tokens: statusBurst, updated: now}
	}
	bucket.tokens = min(float64(statusBurst), bucket.tokens+now.Sub(bucket.updated).Seconds()*statusQueriesPerMinute/60)
	bucket.updated = now
	if bucket.tokens < 1 {
		a.statusBuckets[key] = bucket
		a.statusMu.Unlock()
		return nil, &syncing.Error{Status: 429, Code: "status_rate_exceeded", RetryAfterSeconds: 1}
	}
	bucket.tokens--
	a.statusBuckets[key] = bucket
	a.statusMu.Unlock()
	select {
	case a.statusSlots <- struct{}{}:
		return func() { <-a.statusSlots }, nil
	default:
		return nil, &syncing.Error{Status: 429, Code: "status_capacity_exceeded", RetryAfterSeconds: 1}
	}
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

// Correlation stays possible without emitting client-controlled full UUIDs.
func logIdentity(value string) string {
	if value == "" {
		return ""
	}
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])
}
