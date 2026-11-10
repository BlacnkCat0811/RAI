package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// MarkAnalysisDirty records one plain analysis dirty range inside the caller's
// transaction. It exists so the ordinary per-scope sites share one argument
// order and error shape. Callers that must derive dirty rows with bespoke SQL
// (workout-row subqueries, dataset-calendar truncation, load-day candidates)
// keep those statements local instead of forcing them through this helper.
func MarkAnalysisDirty(ctx context.Context, tx pgx.Tx, dataset, scope, kind string, from, through time.Time) error {
	_, err := tx.Exec(ctx, `SELECT rai_analysis_dirty($1,$2,$3,$4,$5)`, dataset, scope, kind, from, through)
	return err
}
