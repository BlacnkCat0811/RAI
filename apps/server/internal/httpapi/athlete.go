package httpapi

import (
	"context"
	"encoding/base64"
	"net/http"
	"strconv"
	"time"

	am "rai/server/internal/athletemodel"
	diag "rai/server/internal/diagnostics"
	"rai/server/internal/syncing"
)

type athleteModelReader interface {
	ReadAthleteProduct(context.Context, syncing.Principal, time.Time) (am.AthleteModelSnapshot, error)
	ReadAthleteHistory(context.Context, syncing.Principal, string, string, string, int, string, string) (am.HistoryPage, error)
}

type athleteDiagnosticsReader interface {
	ReadAthleteDiagnostics(context.Context, syncing.Principal, time.Time) (diag.ProductSnapshot, error)
}

var athleteMetrics = map[string]bool{"trainingReadiness": true, "physiologicalRecovery": true, "trainingLoad": true, "runningAbility": true, "raiVO2Max": true, "externalVO2Max": true, "sessionRecovery": true, "loadFocus": true, "abilityEstimate": true}

func (a *API) athleteProduct(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	rd, ok := a.store.(athleteModelReader)
	if !ok {
		return syncing.Fail(503, "athlete_model_unavailable")
	}
	q := r.URL.Query()
	if len(q) > 1 || len(q) == 1 && (len(q["schemaVersion"]) != 1 || q.Get("schemaVersion") != am.Schema3) {
		return syncing.Fail(422, "invalid_athlete_query")
	}
	var snapshot am.AthleteModelSnapshot
	var err error
	if q.Get("schemaVersion") == am.Schema3 {
		v3, ok := a.store.(interface {
			ReadAthleteProductV3(context.Context, syncing.Principal, time.Time) (am.AthleteModelSnapshot, error)
		})
		if !ok {
			return syncing.Fail(503, "athlete_model_unavailable")
		}
		snapshot, err = v3.ReadAthleteProductV3(r.Context(), p, time.Now().UTC())
	} else {
		snapshot, err = rd.ReadAthleteProduct(r.Context(), p, time.Now().UTC())
	}
	if err != nil {
		if err == am.ErrNotActivated {
			return syncing.Fail(409, "not_product_activated")
		}
		return syncing.Fail(503, "athlete_model_unavailable")
	}
	respond(w, 200, snapshot)
	return nil
}

func (a *API) athleteDiagnostics(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	rd, ok := a.store.(athleteDiagnosticsReader)
	if !ok {
		return syncing.Fail(503, "athlete_diagnostics_unavailable")
	}
	if len(r.URL.Query()) != 0 {
		return syncing.Fail(422, "invalid_athlete_query")
	}
	snapshot, err := rd.ReadAthleteDiagnostics(r.Context(), p, time.Now().UTC())
	if err != nil {
		if err == am.ErrNotActivated {
			return syncing.Fail(409, "not_product_activated")
		}
		return syncing.Fail(503, "athlete_diagnostics_unavailable")
	}
	respond(w, 200, snapshot)
	return nil
}

func (a *API) athleteHistory(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	rd, ok := a.store.(athleteModelReader)
	if !ok {
		return syncing.Fail(503, "athlete_model_unavailable")
	}
	q := r.URL.Query()
	allowed := map[string]bool{"metric": true, "from": true, "to": true, "limit": true, "cursor": true, "view": true}
	for k, v := range q {
		if !allowed[k] || len(v) != 1 {
			return syncing.Fail(422, "invalid_athlete_history_query")
		}
	}
	metric := q.Get("metric")
	if !athleteMetrics[metric] {
		return syncing.Fail(422, "invalid_metric")
	}
	view := q.Get("view")
	if view == "" {
		view = "revised"
	}
	if view != "revised" && view != "as_known" {
		return syncing.Fail(422, "invalid_view")
	}
	limit := 30
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n < 1 || n > 90 {
			return syncing.Fail(422, "invalid_limit")
		}
		limit = n
	}
	from, to := q.Get("from"), q.Get("to")
	for _, d := range []string{from, to} {
		if d != "" {
			if _, err := time.Parse("2006-01-02", d); err != nil {
				return syncing.Fail(422, "invalid_date")
			}
		}
	}
	cursor := q.Get("cursor")
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || len(raw) > 1024 {
			return syncing.Fail(422, "invalid_cursor")
		}
	}
	page, err := rd.ReadAthleteHistory(r.Context(), p, metric, from, to, limit, cursor, view)
	if err != nil {
		if err == am.ErrHistoryCursor {
			return syncing.Fail(409, "history_cursor_mismatch")
		}
		if err == am.ErrHistoricalInputUnavailable {
			return syncing.Fail(409, "historical_input_unavailable")
		}
		if err == am.ErrUnsupportedRelease {
			return syncing.Fail(409, "model_version_unsupported")
		}
		if err == am.ErrNotActivated {
			return syncing.Fail(409, "not_product_activated")
		}
		return syncing.Fail(503, "athlete_model_unavailable")
	}
	respond(w, 200, page)
	return nil
}
