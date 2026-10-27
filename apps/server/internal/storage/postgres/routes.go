package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
)

// RoutePoint is the deliberately limited P2 staging representation. The P3
// transport contract must reject fields it cannot preserve.
type RoutePoint struct {
	Sequence           int64     `json:"sequence"`
	RecordedAt         time.Time `json:"recorded_at"`
	Latitude           float64   `json:"latitude"`
	Longitude          float64   `json:"longitude"`
	Altitude           *float64  `json:"altitude,omitempty"`
	HorizontalAccuracy *float64  `json:"horizontal_accuracy,omitempty"`
	VerticalAccuracy   *float64  `json:"vertical_accuracy,omitempty"`
	Speed              *float64  `json:"speed,omitempty"`
	Course             *float64  `json:"course,omitempty"`
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func validRoutePoint(p RoutePoint) bool {
	if p.Sequence < 0 || p.RecordedAt.IsZero() || !finite(p.Latitude) || p.Latitude < -90 || p.Latitude > 90 || !finite(p.Longitude) || p.Longitude < -180 || p.Longitude > 180 {
		return false
	}
	for _, v := range []*float64{p.Altitude, p.HorizontalAccuracy, p.VerticalAccuracy, p.Speed, p.Course} {
		if v != nil && !finite(*v) {
			return false
		}
	}
	return (p.HorizontalAccuracy == nil || *p.HorizontalAccuracy >= 0) &&
		(p.VerticalAccuracy == nil || *p.VerticalAccuracy >= 0) &&
		(p.Speed == nil || *p.Speed >= 0) && (p.Course == nil || *p.Course >= 0 && *p.Course < 360)
}

// StageRouteChunk inserts unpublished route data in one transaction. It is not
// a sync endpoint or a durable route receipt: P3 must add deletion barriers,
// complete manifests, sealing, revision logging and transactional enqueue.
// Retries of the same chunk must have identical content and parent identity.
func (s *Store) StageRouteChunk(ctx context.Context, dataset, workout, route, source string, index int32, points []RoutePoint) (bool, error) {
	if source == "" || index < 0 || len(points) == 0 || len(points) > 5000 {
		return false, errors.New("invalid route chunk")
	}
	points = append([]RoutePoint(nil), points...)
	for i := range points {
		if !validRoutePoint(points[i]) || i > 0 && points[i].Sequence <= points[i-1].Sequence {
			return false, errors.New("invalid route point")
		}
		points[i].RecordedAt = points[i].RecordedAt.UTC().Round(0)
	}
	canonical, err := json.Marshal(points)
	if err != nil {
		return false, err
	}
	digest := sha256.Sum256(canonical)
	tx, err := s.begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id FROM datasets WHERE id=$1 FOR UPDATE`, dataset).Scan(&locked); err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO workout_routes(dataset_id,route_uuid,workout_uuid,source_bundle_id) VALUES($1,$2,$3,$4) ON CONFLICT(dataset_id,route_uuid) DO NOTHING`, dataset, route, workout, source)
	if err != nil {
		return false, err
	}
	var identityMatches bool
	var state string
	if err = tx.QueryRow(ctx, `SELECT workout_uuid=$3::uuid AND source_bundle_id=$4,state FROM workout_routes WHERE dataset_id=$1 AND route_uuid=$2 FOR UPDATE`, dataset, route, workout, source).Scan(&identityMatches, &state); err != nil {
		return false, err
	}
	if !identityMatches || state != "pending" {
		return false, errors.New("route identity conflict or sealed route")
	}
	result, err := tx.Exec(ctx, `INSERT INTO route_chunks(dataset_id,route_uuid,chunk_index,point_count,content_sha256,points) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(dataset_id,route_uuid,chunk_index) DO NOTHING`, dataset, route, index, len(points), digest[:], canonical)
	if err != nil {
		return false, err
	}
	if result.RowsAffected() == 0 {
		var old []byte
		if err = tx.QueryRow(ctx, `SELECT content_sha256 FROM route_chunks WHERE dataset_id=$1 AND route_uuid=$2 AND chunk_index=$3`, dataset, route, index).Scan(&old); err != nil {
			return false, err
		}
		if !bytes.Equal(old, digest[:]) {
			return false, errors.New("route chunk conflict")
		}
		return false, tx.Commit(ctx)
	}
	rows := make([][]any, len(points))
	for i, p := range points {
		rows[i] = []any{dataset, route, index, p.Sequence, p.RecordedAt, p.Latitude, p.Longitude, p.Altitude, p.HorizontalAccuracy, p.VerticalAccuracy, p.Speed, p.Course}
	}
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"route_points"}, []string{"dataset_id", "route_uuid", "chunk_index", "sequence", "recorded_at", "latitude", "longitude", "altitude", "horizontal_accuracy", "vertical_accuracy", "speed", "course"}, pgx.CopyFromRows(rows))
	if err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
