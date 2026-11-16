package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"rai/server/internal/syncing"
	"rai/server/internal/workoutdetail"
)

type workoutDetailReader interface {
	WorkoutDetail(context.Context, syncing.Principal, string) (workoutdetail.Detail, error)
	WorkoutSeries(context.Context, syncing.Principal, string) (workoutdetail.SeriesResponse, error)
	WorkoutFreshness(context.Context, syncing.Principal, string) (workoutdetail.Freshness, error)
}

func (a *API) workoutDetail(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	if len(r.URL.Query()) != 0 {
		return syncing.Fail(422, "invalid_workout_query")
	}
	store, ok := a.store.(workoutDetailReader)
	if !ok {
		return syncing.Fail(503, "workout_detail_unavailable")
	}
	id, err := pathID(r)
	if err != nil {
		return err
	}
	v, err := store.WorkoutDetail(r.Context(), p, id)
	if err == nil {
		respond(w, 200, v)
	}
	return err
}

func (a *API) workoutSeries(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	if len(r.URL.Query()) != 0 {
		return syncing.Fail(422, "invalid_workout_query")
	}
	store, ok := a.store.(workoutDetailReader)
	if !ok {
		return syncing.Fail(503, "workout_detail_unavailable")
	}
	id, err := pathID(r)
	if err != nil {
		return err
	}
	v, err := store.WorkoutSeries(r.Context(), p, id)
	if err == nil {
		respond(w, 200, v)
	}
	return err
}

func (a *API) workoutFreshness(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	if len(r.URL.Query()) != 0 {
		return syncing.Fail(422, "invalid_workout_query")
	}
	store, ok := a.store.(workoutDetailReader)
	if !ok {
		return syncing.Fail(503, "workout_detail_unavailable")
	}
	id, err := pathID(r)
	if err != nil {
		return err
	}
	v, err := store.WorkoutFreshness(r.Context(), p, id)
	if err == nil {
		respond(w, 200, v)
	}
	return err
}

type workoutPaceReader interface {
	WorkoutPaceAnalysis(context.Context, syncing.Principal, string) (workoutdetail.PaceAnalysisResponse, error)
}

func (a *API) workoutPaceAnalysis(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	if len(r.URL.Query()) != 0 {
		return syncing.Fail(422, "invalid_workout_query")
	}
	store, ok := a.store.(workoutPaceReader)
	if !ok {
		return syncing.Fail(503, "workout_detail_unavailable")
	}
	id, err := pathID(r)
	if err != nil {
		return err
	}
	v, err := store.WorkoutPaceAnalysis(r.Context(), p, id)
	if err == nil {
		respond(w, 200, v)
	}
	return err
}

type workoutDistanceReader interface {
	WorkoutDistanceDistributions(context.Context, syncing.Principal, time.Time, time.Time) ([]workoutdetail.WorkoutDistanceItem, error)
}

// workoutDistanceDistributions serves the statistics page: the distance
// distribution of every running Workout starting in [fromUs, toUs).
func (a *API) workoutDistanceDistributions(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	q := r.URL.Query()
	if len(q) != 2 || len(q["fromUs"]) != 1 || len(q["toUs"]) != 1 {
		return syncing.Fail(422, "invalid_statistics_query")
	}
	from, e1 := strconv.ParseInt(q.Get("fromUs"), 10, 64)
	to, e2 := strconv.ParseInt(q.Get("toUs"), 10, 64)
	if e1 != nil || e2 != nil || from < 0 || to <= from {
		return syncing.Fail(422, "invalid_statistics_query")
	}
	store, ok := a.store.(workoutDistanceReader)
	if !ok {
		return syncing.Fail(503, "workout_detail_unavailable")
	}
	items, err := store.WorkoutDistanceDistributions(r.Context(), p, time.UnixMicro(from), time.UnixMicro(to))
	if err == nil {
		respond(w, 200, map[string]any{"items": items})
	}
	return err
}
