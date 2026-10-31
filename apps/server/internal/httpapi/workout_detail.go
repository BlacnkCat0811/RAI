package httpapi

import (
	"context"
	"net/http"

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
