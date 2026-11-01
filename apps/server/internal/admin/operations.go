package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
)

// Operations is an explicit maintenance command, outside the online graph.
// No command deletes facts, receipts, barriers, jobs, or immutable history.
func Operations(ctx context.Context, url string, args []string, out io.Writer) error {
	if len(args) != 1 || (args[0] != "snapshot" && args[0] != "verify" && args[0] != "retention-dry-run") {
		return errors.New("usage: rai ops snapshot|verify|retention-dry-run")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return errors.New("operations connection failed")
	}
	defer conn.Close(ctx)
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return errors.New("operations transaction failed")
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='60s'; SET LOCAL lock_timeout='1s'`); err != nil {
		return errors.New("operations setup failed")
	}
	var result any
	if args[0] == "snapshot" {
		result, err = operationsSnapshot(ctx, tx)
	} else {
		result, err = operationsInventory(ctx, tx, args[0] == "verify")
	}
	if err != nil {
		return errors.New("operations inspection failed")
	}
	if err = tx.Commit(ctx); err != nil {
		return errors.New("operations inspection commit failed")
	}
	return json.NewEncoder(out).Encode(result)
}

func operationsSnapshot(ctx context.Context, tx pgx.Tx) (any, error) {
	// Aggregate-only output: no dataset/device/workout IDs or health values.
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT jsonb_build_object(
 'observed_at',clock_timestamp(), 'database_size_bytes',pg_database_size(current_database()),
 'connections',(SELECT count(*) FROM pg_stat_activity WHERE datname=current_database()),
 'lock_waiters',(SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'),
 'rollbacks_total',(SELECT xact_rollback FROM pg_stat_database WHERE datname=current_database()),
 'deadlocks_total',(SELECT deadlocks FROM pg_stat_database WHERE datname=current_database()),
 'jobs',(SELECT coalesce(jsonb_object_agg(state,n),'{}') FROM (SELECT state,count(*) n FROM jobs GROUP BY state) j),
 'failed_jobs_needing_action',(SELECT count(*) FROM jobs j LEFT JOIN dirty_scopes d ON d.dataset_id=j.dataset_id AND d.scope_key=j.scope_key
   WHERE j.state='repair_required' OR (j.state='failed' AND NOT coalesce(d.generation>j.target_generation AND d.clean_generation>=j.target_generation,false))),
 'historical_failed_jobs',(SELECT count(*) FROM jobs j JOIN dirty_scopes d ON d.dataset_id=j.dataset_id AND d.scope_key=j.scope_key
   WHERE j.state='failed' AND d.generation>j.target_generation AND d.clean_generation>=j.target_generation),
 'expired_leases',(SELECT count(*) FROM jobs WHERE state='running' AND lease_until<clock_timestamp()),
 'retry_count',(SELECT coalesce(sum(greatest(attempts-1,0)),0) FROM jobs),
 'oldest_due_job_seconds',(SELECT coalesce(greatest(extract(epoch FROM clock_timestamp()-min(run_after)),0),0) FROM jobs WHERE state='queued'),
 'training_backlog',(SELECT count(*) FROM jobs WHERE kind='training.plan' AND state IN ('queued','running')),
 'feedback_pending',(SELECT count(*) FROM training_executions WHERE status='completed' AND workout_id IS NULL),
 'generation_lag',(SELECT coalesce(max(generation-clean_generation),0) FROM dirty_scopes),
 'analysis_publication_age_seconds',(SELECT extract(epoch FROM clock_timestamp()-max(published_at)) FROM analysis_publications WHERE metric_family='state'),
 'analysis_runs',(SELECT coalesce(jsonb_object_agg(status,n),'{}') FROM (SELECT status,count(*) n FROM analysis_runs GROUP BY status) a),
 'training_runs',(SELECT coalesce(jsonb_object_agg(status,n),'{}') FROM (SELECT status,count(*) n FROM training_generation_runs GROUP BY status) t),
 'tables',(SELECT coalesce(jsonb_agg(jsonb_build_object('table',relname,'bytes',pg_total_relation_size(relid),'estimated_rows',n_live_tup,'dead_tuples',n_dead_tup,'last_autovacuum',last_autovacuum) ORDER BY relname),'[]') FROM pg_stat_user_tables WHERE schemaname='public'))`).Scan(&raw)
	return json.RawMessage(raw), err
}

type TableInventory struct {
	Table  string `json:"table"`
	Rows   int64  `json:"rows"`
	SHA256 string `json:"sha256,omitempty"`
}
type Inventory struct {
	SchemaVersion       int64            `json:"schema_version"`
	Tables              []TableInventory `json:"tables"`
	InvariantViolations int64            `json:"invariant_violations"`
	AutomaticDeletion   bool             `json:"automatic_deletion"`
}

func operationsInventory(ctx context.Context, tx pgx.Tx, verify bool) (Inventory, error) {
	result := Inventory{}
	if err := tx.QueryRow(ctx, `SELECT version_id FROM goose_db_version WHERE is_applied ORDER BY id DESC LIMIT 1`).Scan(&result.SchemaVersion); err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, `SELECT c.relname, (SELECT string_agg(quote_ident(a.attname),',' ORDER BY k.n) FROM pg_index i CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY k(attnum,n) JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=k.attnum WHERE i.indrelid=c.oid AND i.indisprimary) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind='r' ORDER BY c.relname`)
	if err != nil {
		return result, err
	}
	type table struct {
		name  string
		order *string
	}
	var tables []table
	for rows.Next() {
		var t table
		if err = rows.Scan(&t.name, &t.order); err != nil {
			rows.Close()
			return result, err
		}
		tables = append(tables, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	for _, t := range tables {
		item := TableInventory{Table: t.name}
		name := pgx.Identifier{"public", t.name}.Sanitize()
		if !verify {
			err = tx.QueryRow(ctx, "SELECT count(*) FROM "+name).Scan(&item.Rows)
		} else {
			order := "to_jsonb(t)::text"
			if t.order != nil {
				order = *t.order
			}
			r, e := tx.Query(ctx, "SELECT to_jsonb(t)::text FROM "+name+" t ORDER BY "+order)
			if e != nil {
				return result, e
			}
			h := sha256.New()
			for r.Next() {
				var b string
				if e = r.Scan(&b); e != nil {
					break
				}
				_, _ = io.WriteString(h, b+"\n")
				item.Rows++
			}
			if e == nil {
				e = r.Err()
			}
			r.Close()
			if e != nil {
				return result, e
			}
			item.SHA256 = hex.EncodeToString(h.Sum(nil))
		}
		if err != nil {
			return result, err
		}
		result.Tables = append(result.Tables, item)
	}
	if verify {
		err = tx.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM pg_constraint WHERE connamespace='public'::regnamespace AND contype='f' AND NOT convalidated)+
 (SELECT count(*) FROM health_samples s JOIN entity_tombstones t USING(dataset_id,sample_type,sample_uuid))+
 (SELECT count(*) FROM workouts w JOIN entity_tombstones t ON t.dataset_id=w.dataset_id AND t.sample_type='HKWorkoutTypeIdentifier' AND t.sample_uuid=w.workout_uuid)+
 (SELECT count(*) FROM dirty_scopes WHERE clean_generation>generation)+
 (SELECT count(*) FROM analysis_publications p JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id WHERE r.status<>'SUCCEEDED')+
 (SELECT count(*) FROM sync_batches WHERE (state='committed')<>(receipt IS NOT NULL AND committed_at IS NOT NULL))+
 (SELECT count(*) FROM sync_streams s WHERE s.last_client_seq>0 AND NOT EXISTS(SELECT 1 FROM sync_batches b WHERE b.dataset_id=s.dataset_id AND b.stream_id=s.id AND b.batch_id=s.last_batch_id AND b.client_seq=s.last_client_seq AND b.state='committed'))+
 (SELECT count(*) FROM workout_routes WHERE state='sealed' AND receipt IS NULL)+
 (SELECT count(*) FROM workout_routes w JOIN entity_tombstones t ON t.dataset_id=w.dataset_id AND t.sample_type='HKWorkoutRouteTypeIdentifier' AND t.sample_uuid=w.route_uuid)+
 (SELECT count(*) FROM training_plans p LEFT JOIN training_effective_versions e ON e.dataset_id=p.dataset_id AND e.plan_id=p.id AND e.id=p.current_effective_id WHERE (p.current_base_id IS NULL)<>(p.current_effective_id IS NULL) OR (e.id IS NOT NULL AND (e.base_id<>p.current_base_id OR e.override_revision<>p.override_revision)))+
 (SELECT count(*) FROM analysis_publications p JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id WHERE p.generation<>r.target_generation)+
 (SELECT count(*) FROM sync_streams s WHERE s.last_client_seq<>(SELECT count(*) FROM sync_batches b WHERE b.dataset_id=s.dataset_id AND b.stream_id=s.id AND b.state='committed'))`).Scan(&result.InvariantViolations)
		if err == nil && (result.SchemaVersion != 19 || result.InvariantViolations != 0) {
			err = errors.New("restored invariants failed")
		}
	}
	return result, err
}
