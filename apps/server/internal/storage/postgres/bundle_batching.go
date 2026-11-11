package postgres

import (
	"bytes"
	"context"
	"math"
	"time"

	"rai/server/internal/syncing"

	"github.com/jackc/pgx/v5"
)

// Set-based bundle storage.
//
// The import path must keep one statement per bounded batch, never one
// statement per sample: a bundle with tens of thousands of samples otherwise
// costs tens of thousands of round trips while the dataset row lock is held.
// Each statement below is capped by row count and by the bytes it carries, and
// every decision it feeds keeps the per-key semantics, ordering and conflict
// code of the scalar checks it replaces.

// SampleIdentityStatement is the set-based identity lookup the import
// transaction runs for every sample batch. It is a package constant so the
// write-path measurement harness can EXPLAIN exactly the production text.
const SampleIdentityStatement = `SELECT k.sample_type,k.sample_uuid::text,
				(h.sample_uuid IS NOT NULL),
				EXISTS(SELECT 1 FROM entity_tombstones t
					WHERE t.dataset_id=$1 AND t.sample_type=k.sample_type AND t.sample_uuid=k.sample_uuid::uuid),
				COALESCE(w.workout_uuid::text,''),
				h.content_sha256,h.event_start,h.event_end,h.unit,h.value_numeric,h.value_category,h.source_bundle_id
			FROM unnest($2::text[],$3::text[]) AS k(sample_type,sample_uuid)
			LEFT JOIN health_samples h
				ON h.dataset_id=$1 AND h.sample_type=k.sample_type AND h.sample_uuid=k.sample_uuid::uuid
			LEFT JOIN workout_samples w
				ON w.dataset_id=$1 AND w.sample_type=k.sample_type AND w.sample_uuid=k.sample_uuid::uuid`

const (
	// bundleBatchRows bounds the keys or rows of one set-based statement.
	bundleBatchRows = 1000
	// bundleBatchParamBytes bounds the text/JSON bytes one statement carries.
	bundleBatchParamBytes = 4 << 20
)

// batchIndexes splits count rows into contiguous batches bounded by row count
// and by rowBytes. A single row larger than the byte budget still forms its own
// batch, so no batch is ever empty.
func batchIndexes(count int, rowBytes func(int) int) [][]int {
	if count == 0 {
		return nil
	}
	batches := make([][]int, 0, count/bundleBatchRows+1)
	for start := 0; start < count; {
		end := start
		size := 0
		for end < count && end-start < bundleBatchRows {
			size += rowBytes(end)
			if end > start && size > bundleBatchParamBytes {
				break
			}
			end++
		}
		indexes := make([]int, end-start)
		for i := start; i < end; i++ {
			indexes[i-start] = i
		}
		batches = append(batches, indexes)
		start = end
	}
	return batches
}

// contiguous returns the source slice a contiguous batch index range covers.
func contiguous[T any](items []T, batch []int) []T {
	return items[batch[0] : batch[len(batch)-1]+1]
}

func anyTrue(values []bool) bool {
	for _, value := range values {
		if value {
			return true
		}
	}
	return false
}

type sampleKey struct {
	sampleType string
	sampleUUID string
}

// storedSample is the committed state of one bundle sample key. One set query
// returns every fact the scalar path fetched per sample.
type storedSample struct {
	exists    bool
	tombstone bool
	workout   string
	hash      []byte
	start     time.Time
	end       time.Time
	unit      *string
	value     *float64
	category  *int32
	source    string
}

// loadSampleIdentities resolves every bundle sample against committed facts in
// one bounded set statement per batch: deletion barrier, stored content
// identity, stored core fields and existing Workout association.
func loadSampleIdentities(ctx context.Context, tx pgx.Tx, dataset string, rows []sampleRow) (map[sampleKey]storedSample, error) {
	stored := make(map[sampleKey]storedSample, len(rows))
	for _, batch := range batchIndexes(len(rows), func(i int) int {
		return len(rows[i].event.Type) + len(rows[i].event.UUID) + 64
	}) {
		types := make([]string, len(batch))
		uuids := make([]string, len(batch))
		for n, index := range batch {
			types[n], uuids[n] = rows[index].event.Type, rows[index].event.UUID
		}
		query, err := tx.Query(ctx, SampleIdentityStatement, dataset, types, uuids)
		if err != nil {
			return nil, err
		}
		for query.Next() {
			var key sampleKey
			var state storedSample
			var start, end *time.Time
			var source *string
			if err = query.Scan(&key.sampleType, &key.sampleUUID, &state.exists, &state.tombstone, &state.workout,
				&state.hash, &start, &end, &state.unit, &state.value, &state.category, &source); err != nil {
				query.Close()
				return nil, err
			}
			if start != nil {
				state.start = *start
			}
			if end != nil {
				state.end = *end
			}
			if source != nil {
				state.source = *source
			}
			stored[key] = state
		}
		query.Close()
		if err = query.Err(); err != nil {
			return nil, err
		}
	}
	return stored, nil
}

// sameSampleCore is the Go form of the scalar core-identity comparison:
// event_start = $start AND event_end = $end AND unit IS NOT DISTINCT FROM $unit
// AND float8send(value_numeric) IS NOT DISTINCT FROM float8send($value)
// AND value_category IS NOT DISTINCT FROM $category AND source_bundle_id = $source.
// It only ever allows append-only enrichment of an identical stored fact.
func sameSampleCore(state storedSample, event syncing.Event) bool {
	if !state.exists || event.StartUs == nil || event.EndUs == nil || event.Source == nil {
		return false
	}
	if !state.start.Equal(time.UnixMicro(*event.StartUs).UTC()) || !state.end.Equal(time.UnixMicro(*event.EndUs).UTC()) {
		return false
	}
	var unit *string
	var value *float64
	if event.Quantity != nil {
		unit, value = &event.Quantity.Unit, &event.Quantity.Value
	}
	if !sameOptionalText(state.unit, unit) || !sameFloat8Bits(state.value, value) ||
		!sameOptionalInt(state.category, event.Category) {
		return false
	}
	return state.source == event.Source.BundleID
}

func sameOptionalText(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// sameFloat8Bits keeps the float8send byte comparison of the scalar query:
// equal bits, not numeric equality, so -0.0 and NaN are never conflated.
func sameFloat8Bits(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return math.Float64bits(*a) == math.Float64bits(*b)
}

func sameOptionalInt(a *int32, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return int64(*a) == int64(*b)
}

type storedSeries struct {
	exists      bool
	seriesID    string
	state       string
	totalPoints int64
	chunkCount  int
}

// seriesIdentity resolves every bundle series in bounded set queries and
// returns, per bundle position, whether that series still needs to be written.
// Row-level identity and chunk hashes keep their original conflict code, and
// chunk hashes are compared in chunk_index order exactly as before.
func seriesIdentity(ctx context.Context, tx pgx.Tx, dataset string, series []syncing.WorkoutBundleSeries) ([]bool, error) {
	write := make([]bool, len(series))
	if len(series) == 0 {
		return write, nil
	}
	stored := make(map[sampleKey]storedSeries, len(series))
	for _, batch := range batchIndexes(len(series), func(i int) int {
		return len(series[i].SampleType) + len(series[i].SampleUUID) + 64
	}) {
		types := make([]string, len(batch))
		uuids := make([]string, len(batch))
		for n, index := range batch {
			types[n], uuids[n] = series[index].SampleType, series[index].SampleUUID
		}
		query, err := tx.Query(ctx, `SELECT k.sample_type,k.sample_uuid::text,
				s.series_id::text,s.state,s.total_points,COALESCE(s.chunk_count,0)
			FROM unnest($2::text[],$3::text[]) AS k(sample_type,sample_uuid)
			LEFT JOIN series_uploads s
				ON s.dataset_id=$1 AND s.sample_type=k.sample_type AND s.sample_uuid=k.sample_uuid::uuid`,
			dataset, types, uuids)
		if err != nil {
			return nil, err
		}
		for query.Next() {
			var key sampleKey
			var state storedSeries
			var seriesID, uploadState *string
			var totalPoints *int64
			var chunkCount *int32
			if err = query.Scan(&key.sampleType, &key.sampleUUID, &seriesID, &uploadState, &totalPoints, &chunkCount); err != nil {
				query.Close()
				return nil, err
			}
			if seriesID != nil {
				state.exists = true
				state.seriesID = *seriesID
				if uploadState != nil {
					state.state = *uploadState
				}
				if totalPoints != nil {
					state.totalPoints = *totalPoints
				}
				if chunkCount != nil {
					state.chunkCount = int(*chunkCount)
				}
			}
			stored[key] = state
		}
		query.Close()
		if err = query.Err(); err != nil {
			return nil, err
		}
	}
	hashes, err := seriesChunkHashes(ctx, tx, dataset, storedSeriesIDs(stored))
	if err != nil {
		return nil, err
	}
	for index, item := range series {
		state := stored[sampleKey{sampleType: item.SampleType, sampleUUID: item.SampleUUID}]
		if !state.exists {
			write[index] = true
			continue
		}
		if state.state != "sealed" || state.totalPoints != int64(len(item.Points)) || state.chunkCount != bundleSeriesChunkCount(item.Points) {
			return nil, syncing.Fail(409, "series_identity_conflict")
		}
		chunks := bundleSeriesChunks(item.Points)
		digests := hashes[state.seriesID]
		if len(digests) != len(chunks) {
			return nil, syncing.Fail(409, "series_identity_conflict")
		}
		for position, chunk := range chunks {
			_, digest := syncing.SeriesPointsCanonical(chunk)
			if !bytes.Equal(digests[position], digest) {
				return nil, syncing.Fail(409, "series_identity_conflict")
			}
		}
	}
	return write, nil
}

func storedSeriesIDs(stored map[sampleKey]storedSeries) []string {
	ids := make([]string, 0, len(stored))
	for _, state := range stored {
		if state.exists {
			ids = append(ids, state.seriesID)
		}
	}
	return ids
}

type storedRoute struct {
	exists      bool
	workout     string
	source      string
	state       string
	totalPoints int64
}

// routeIdentity resolves every bundle route in bounded set queries and returns,
// per bundle position, whether that route still needs to be written. The
// deletion barrier is checked before identity, in bundle order, as before.
func routeIdentity(ctx context.Context, tx pgx.Tx, dataset, workoutID string, routes []syncing.WorkoutBundleRoute) ([]bool, error) {
	write := make([]bool, len(routes))
	if len(routes) == 0 {
		return write, nil
	}
	ids := make([]string, len(routes))
	for index, route := range routes {
		ids[index] = route.RouteID
	}
	deleted := make(map[string]bool, len(routes))
	for _, batch := range batchIndexes(len(ids), func(i int) int { return len(ids[i]) + 32 }) {
		query, err := tx.Query(ctx, `SELECT t.sample_uuid::text FROM entity_tombstones t
			WHERE t.dataset_id=$1 AND t.sample_type=$2
				AND t.sample_uuid IN (SELECT x::uuid FROM unnest($3::text[]) AS x)`,
			dataset, syncing.RouteType, contiguous(ids, batch))
		if err != nil {
			return nil, err
		}
		for query.Next() {
			var id string
			if err = query.Scan(&id); err != nil {
				query.Close()
				return nil, err
			}
			deleted[id] = true
		}
		query.Close()
		if err = query.Err(); err != nil {
			return nil, err
		}
	}
	stored := make(map[string]storedRoute, len(routes))
	for _, batch := range batchIndexes(len(ids), func(i int) int { return len(ids[i]) + 32 }) {
		query, err := tx.Query(ctx, `SELECT k.route_uuid::text,
				r.workout_uuid::text,r.source_bundle_id,r.state,COALESCE(r.total_points,0)
			FROM unnest($2::text[]) AS k(route_uuid)
			LEFT JOIN workout_routes r ON r.dataset_id=$1 AND r.route_uuid=k.route_uuid::uuid`,
			dataset, contiguous(ids, batch))
		if err != nil {
			return nil, err
		}
		for query.Next() {
			var id string
			var state storedRoute
			var workout, source, routeState *string
			var totalPoints *int64
			if err = query.Scan(&id, &workout, &source, &routeState, &totalPoints); err != nil {
				query.Close()
				return nil, err
			}
			if workout != nil {
				state.exists = true
				state.workout = *workout
			}
			if source != nil {
				state.source = *source
			}
			if routeState != nil {
				state.state = *routeState
			}
			if totalPoints != nil {
				state.totalPoints = *totalPoints
			}
			stored[id] = state
		}
		query.Close()
		if err = query.Err(); err != nil {
			return nil, err
		}
	}
	hashes, err := routeChunkHashes(ctx, tx, dataset, ids)
	if err != nil {
		return nil, err
	}
	for index, route := range routes {
		if deleted[route.RouteID] {
			return nil, syncing.Fail(409, "route_deleted")
		}
		state := stored[route.RouteID]
		if !state.exists {
			write[index] = true
			continue
		}
		if state.workout != workoutID || state.source != route.Source.BundleID || state.state != "sealed" ||
			state.totalPoints != int64(len(route.Points)) {
			return nil, syncing.Fail(409, "route_identity_conflict")
		}
		chunks := bundleRouteChunks(route)
		digests := hashes[route.RouteID]
		if len(digests) != len(chunks) {
			return nil, syncing.Fail(409, "route_identity_conflict")
		}
		for position, chunk := range chunks {
			_, digest := syncing.RoutePointsCanonical(chunk)
			if !bytes.Equal(digests[position], digest) {
				return nil, syncing.Fail(409, "route_identity_conflict")
			}
		}
	}
	return write, nil
}

// seriesChunkHashes reads stored chunk digests for a set of series ids, in
// chunk_index order so each series keeps its original chunk order.
func seriesChunkHashes(ctx context.Context, tx pgx.Tx, dataset string, ids []string) (map[string][][]byte, error) {
	hashes := make(map[string][][]byte, len(ids))
	for _, batch := range batchIndexes(len(ids), func(i int) int { return len(ids[i]) + 32 }) {
		query, err := tx.Query(ctx, `SELECT c.series_id::text,c.points_sha256 FROM series_chunks c
			WHERE c.dataset_id=$1 AND c.series_id IN (SELECT x::uuid FROM unnest($2::text[]) AS x)
			ORDER BY c.series_id,c.chunk_index`, dataset, contiguous(ids, batch))
		if err != nil {
			return nil, err
		}
		for query.Next() {
			var id string
			var digest []byte
			if err = query.Scan(&id, &digest); err != nil {
				query.Close()
				return nil, err
			}
			hashes[id] = append(hashes[id], digest)
		}
		query.Close()
		if err = query.Err(); err != nil {
			return nil, err
		}
	}
	return hashes, nil
}

// routeChunkHashes reads stored chunk digests for a set of route ids, in
// chunk_index order so each route keeps its original chunk order.
func routeChunkHashes(ctx context.Context, tx pgx.Tx, dataset string, ids []string) (map[string][][]byte, error) {
	hashes := make(map[string][][]byte, len(ids))
	for _, batch := range batchIndexes(len(ids), func(i int) int { return len(ids[i]) + 32 }) {
		query, err := tx.Query(ctx, `SELECT c.route_uuid::text,c.content_sha256 FROM route_chunks c
			WHERE c.dataset_id=$1 AND c.route_uuid IN (SELECT x::uuid FROM unnest($2::text[]) AS x)
			ORDER BY c.route_uuid,c.chunk_index`, dataset, contiguous(ids, batch))
		if err != nil {
			return nil, err
		}
		for query.Next() {
			var id string
			var digest []byte
			if err = query.Scan(&id, &digest); err != nil {
				query.Close()
				return nil, err
			}
			hashes[id] = append(hashes[id], digest)
		}
		query.Close()
		if err = query.Err(); err != nil {
			return nil, err
		}
	}
	return hashes, nil
}
