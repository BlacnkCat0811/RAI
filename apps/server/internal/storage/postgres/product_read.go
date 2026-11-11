package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"rai/server/internal/product"
	"rai/server/internal/syncing"
)

// Read raw summary facts and only clean, published derived metrics. No raw
// payloads, HR series, pending routes or unpublished runs leave this projection.
// The per-Workout derived metric is the v2 load the active model stack's
// latest day release recorded in model_workout_results.
const productWorkoutFrom = ` FROM workouts w
 LEFT JOIN LATERAL (SELECT total_distance_m FROM workout_enrichments e WHERE e.dataset_id=w.dataset_id AND e.workout_uuid=w.workout_uuid AND total_distance_m IS NOT NULL ORDER BY observed_at_us DESC,source_version DESC LIMIT 1) e ON true
 LEFT JOIN LATERAL (SELECT jsonb_build_array(jsonb_build_object('metric','workout_load','value',(r.load->>'load')::float8,'unit','RAI_AU',
 'quality',r.load->>'quality','algorithm',r.load->>'model','algorithmVersion',r.load->>'modelVersion',
 'details',jsonb_build_object('qualityTier',r.load->>'qualityTier','method',r.load->>'method'))) metrics,
 (r.load->>'load')::float8 load
 FROM model_workout_results r JOIN analysis_model_channels c ON c.dataset_id=r.dataset_id AND c.stack_id=r.stack_id AND c.channel='active'
 WHERE r.dataset_id=w.dataset_id AND r.workout_uuid=w.workout_uuid AND r.load IS NOT NULL) m ON true `
const productDistance = `COALESCE((w.payload->'workout'->'totalDistance'->>'value')::float8,e.total_distance_m)`

func (s *Store) ProductWorkouts(ctx context.Context, p syncing.Principal, before string, limit int, id string) (product.Page, error) {
	out := product.Page{Items: []product.Workout{}}
	if limit < 1 || limit > 50 {
		return out, syncing.Fail(422, "invalid_workout_query")
	}
	stamp := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	cursorID := "ffffffff-ffff-ffff-ffff-ffffffffffff"
	if before != "" {
		b, e := base64.RawURLEncoding.DecodeString(before)
		if e != nil {
			return out, syncing.Fail(422, "invalid_workout_cursor")
		}
		parts := strings.Split(string(b), "|")
		if len(parts) != 2 || !syncing.ValidID(parts[1]) {
			return out, syncing.Fail(422, "invalid_workout_cursor")
		}
		n, e := strconv.ParseInt(parts[0], 10, 64)
		if e != nil || n < 0 {
			return out, syncing.Fail(422, "invalid_workout_cursor")
		}
		stamp = time.UnixMicro(n)
		cursorID = parts[1]
	}
	if id != "" && !syncing.ValidID(id) {
		return out, syncing.Fail(422, "invalid_workout_id")
	}
	rows, e := s.Pool.Query(ctx, `SELECT w.workout_uuid::text,w.event_start,w.activity_type,w.duration_us/1000000.0,`+productDistance+`,COALESCE(m.metrics,'[]'::jsonb)`+productWorkoutFrom+` WHERE w.dataset_id=$1 AND (w.event_start,w.workout_uuid)<($2,$3::uuid) AND ($4='' OR w.workout_uuid=NULLIF($4,'')::uuid) ORDER BY w.event_start DESC,w.workout_uuid DESC LIMIT $5`, p.DatasetID, stamp, cursorID, id, limit+1)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var v product.Workout
		var t time.Time
		var b []byte
		if e = rows.Scan(&v.ID, &t, &v.ActivityType, &v.DurationS, &v.DistanceM, &b); e != nil {
			return out, e
		}
		v.StartUs = t.UnixMicro()
		v.RouteStatus = "unavailable_no_read_api"
		if e = json.Unmarshal(b, &v.Metrics); e != nil {
			return out, e
		}
		v.AnalysisStatus = "pending"
		if len(v.Metrics) > 0 {
			v.AnalysisStatus = "published"
		}
		out.Items = append(out.Items, v)
	}
	if e = rows.Err(); e != nil {
		return out, e
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		last := out.Items[limit-1]
		out.Next = base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(last.StartUs, 10) + "|" + last.ID))
	}
	if id != "" && len(out.Items) == 0 {
		return out, syncing.Fail(404, "workout_not_found")
	}
	return out, nil
}
func (s *Store) ProductOverview(ctx context.Context, p syncing.Principal) (product.Overview, error) {
	out := product.Overview{Days: []product.Day{}}
	// Dataset calendar boundaries, including DST; exactly two calendar weeks.
	rows, e := s.Pool.Query(ctx, `WITH calendar AS (SELECT timezone,(clock_timestamp() AT TIME ZONE timezone)::date today FROM datasets WHERE id=$1), bounds AS (SELECT *,today-((extract(isodow FROM today)::int)-1)-7 first FROM calendar), totals AS (
 SELECT (w.event_start AT TIME ZONE x.timezone)::date AS calendar_day,count(*) n,CASE WHEN count(`+productDistance+`)=count(*) THEN sum(`+productDistance+`) END distance,CASE WHEN count(m.load)=count(*) THEN sum(m.load) END load `+productWorkoutFrom+` CROSS JOIN bounds x WHERE w.dataset_id=$1 AND w.activity_type=37 AND w.event_start>=x.first::timestamp AT TIME ZONE x.timezone AND w.event_start<(x.today+1)::timestamp AT TIME ZONE x.timezone GROUP BY 1)
 SELECT x.timezone,x.today::text,d::date::text,COALESCE(t.n,0),CASE WHEN t.n IS NULL THEN 0 ELSE t.distance END,CASE WHEN t.n IS NULL THEN 0 ELSE t.load END FROM bounds x CROSS JOIN LATERAL generate_series(x.first::timestamp,(x.first+13)::timestamp,interval '1 day') d LEFT JOIN totals t ON t.calendar_day=d::date ORDER BY d`, p.DatasetID)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var d product.Day
		if e = rows.Scan(&out.Timezone, &out.Today, &d.Date, &d.Count, &d.DistanceM, &d.Load); e != nil {
			return out, e
		}
		if d.Date > out.Today {
			d.DistanceM = nil
			d.Load = nil
		}
		out.Days = append(out.Days, d)
	}
	return out, rows.Err()
}
