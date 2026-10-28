package httpapi

import (
	"context"
	"net/http"
	"rai/server/internal/product"
	"rai/server/internal/syncing"
	"strconv"
)

type productReader interface {
	ProductWorkouts(context.Context, syncing.Principal, string, int, string) (product.Page, error)
	ProductOverview(context.Context, syncing.Principal) (product.Overview, error)
	ProductHistory(context.Context, syncing.Principal, int) (product.History, error)
}

func (a *API) product(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	s, ok := a.store.(productReader)
	if !ok {
		return syncing.Fail(503, "product_unavailable")
	}
	q := r.URL.Query()
	for k, v := range q {
		allowed := r.Pattern == "GET /v2/workouts" && (k == "before" || k == "limit") || r.Pattern == "GET /v2/analysis/history" && k == "days"
		if !allowed || len(v) != 1 || v[0] == "" {
			return syncing.Fail(422, "invalid_product_query")
		}
	}
	var out any
	var err error
	switch r.Pattern {
	case "GET /v2/workouts", "GET /v2/workouts/{id}":
		limit := 20
		if q.Has("limit") {
			limit, err = strconv.Atoi(q.Get("limit"))
			if err != nil {
				return syncing.Fail(422, "invalid_workout_query")
			}
		}
		out, err = s.ProductWorkouts(r.Context(), p, q.Get("before"), limit, r.PathValue("id"))
	case "GET /v2/product/overview":
		out, err = s.ProductOverview(r.Context(), p)
	case "GET /v2/analysis/history":
		days := 14
		if q.Has("days") {
			days, err = strconv.Atoi(q.Get("days"))
			if err != nil {
				return syncing.Fail(422, "invalid_analysis_query")
			}
		}
		out, err = s.ProductHistory(r.Context(), p, days)
	}
	if err != nil {
		return err
	}
	respond(w, 200, out)
	return nil
}
