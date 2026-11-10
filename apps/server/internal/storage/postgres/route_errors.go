package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"rai/server/internal/syncing"
)

// routeDBError maps the database errors raised by the route chunk SQL to
// protocol errors. The protocol 6 bundle write path still needs it after the
// legacy v2 route write endpoints were removed.
func routeDBError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case "RA001":
		return syncing.Fail(404, "route_not_found")
	case "RA011":
		return syncing.Fail(409, "route_chunk_gap")
	case "RA012":
		return syncing.Fail(413, "route_chunk_too_large")
	case "RA013":
		return syncing.Fail(409, "route_chunk_conflict")
	case "RA014", "RA016", "RA017":
		return syncing.Fail(422, "unsupported_payload")
	case "RA015":
		return syncing.Fail(413, "route_too_large")
	case "RA021":
		return syncing.Fail(409, "route_incomplete")
	case "RA022":
		return syncing.Fail(409, "route_manifest_mismatch")
	case "RA023":
		return syncing.Fail(409, "route_already_sealed")
	default:
		return err
	}
}
