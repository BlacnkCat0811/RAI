package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"rai/server/internal/syncing"
)

// WriteStrategy selects how the two fact projections of a bundle import
// (health_samples and workout_samples) are written. All three strategies run
// inside the caller's import transaction, keep the same conflict semantics
// (no ON CONFLICT anywhere: a duplicate identity must abort the bundle), and
// bind the same values; only the statement shape differs.
//
// The strategy is measured, not assumed: see
// docs/evidence/workout-sync-speed-round2/server/ for the comparison of the
// three candidates on the normal (non-race) build.
type WriteStrategy string

const (
	// StrategyValues writes one multi-row VALUES tuple per fact in bounded
	// batches. This is the round-1 baseline.
	StrategyValues WriteStrategy = "values"
	// StrategyArrays binds one fixed-shape array parameter per column and
	// expands it with unnest() in a single statement per batch.
	StrategyArrays WriteStrategy = "arrays"
	// StrategyCopy streams the facts into a transaction-local staging table
	// with COPY and then projects them with one set-based INSERT ... SELECT,
	// verifying the projection with a join barrier.
	StrategyCopy WriteStrategy = "copy"
)

// bundleArrayBatchRows bounds one unnest() statement. It is larger than
// bundleBatchRows because an array bind has a fixed parameter count, so the
// only cost of a bigger batch is the size of one statement's argument block.
const bundleArrayBatchRows = 5000

// currentWriteStrategy is process wide so the import path and the comparison
// harness agree; it is only changed by tests and the measurement harness.
//
// The measured default is the array candidate: on the normal build it cut the
// S60 write phases from 1634ms+1215ms to 859ms+873ms at identical facts and
// identical WAL volume, because one fixed-shape unnest bind replaces eleven
// bind parameters per row. COPY lost to it and VALUES stays available as the
// reference shape.
var currentWriteStrategy = StrategyArrays

// CurrentWriteStrategy reports the strategy the import path uses.
func CurrentWriteStrategy() WriteStrategy { return currentWriteStrategy }

// SetWriteStrategy selects the fact-write strategy. It exists so the write-path
// comparison (R5) and the strategy pinning tests can drive every candidate
// through the same production code path.
func SetWriteStrategy(strategy WriteStrategy) error {
	switch strategy {
	case StrategyValues, StrategyArrays, StrategyCopy:
		currentWriteStrategy = strategy
		return nil
	}
	return fmt.Errorf("unknown write strategy %q", strategy)
}

// insertSampleRows writes new health_samples facts with the selected strategy.
func insertSampleRows(ctx context.Context, tx pgx.Tx, dataset string, rows []sampleRow, rev int64) error {
	switch currentWriteStrategy {
	case StrategyArrays:
		return insertSampleRowsArrays(ctx, tx, dataset, rows, rev)
	case StrategyCopy:
		return insertSampleRowsCopy(ctx, tx, dataset, rows, rev)
	default:
		return insertSampleRowsValues(ctx, tx, dataset, rows, rev)
	}
}

// insertAssociationRows writes new workout_samples associations with the
// selected strategy.
func insertAssociationRows(ctx context.Context, tx pgx.Tx, dataset, workoutID string, rows []sampleRow, rev int64) error {
	switch currentWriteStrategy {
	case StrategyArrays:
		return insertAssociationRowsArrays(ctx, tx, dataset, workoutID, rows, rev)
	case StrategyCopy:
		return insertAssociationRowsCopy(ctx, tx, dataset, workoutID, rows, rev)
	default:
		return insertAssociationRowsValues(ctx, tx, dataset, workoutID, rows, rev)
	}
}

// sampleColumns is the column list shared by every sample projection so the
// candidates stay comparable.
const sampleColumns = "dataset_id,sample_type,sample_uuid,event_start,event_end,value_numeric,value_category,unit,source_bundle_id,content_sha256,entity_revision,metadata"

// sampleMetadata marshals the per-sample metadata of every row, normalising a
// nil map to the empty JSON object the column default would produce.
func sampleMetadata(rows []sampleRow) ([]json.RawMessage, error) {
	metadata := make([]json.RawMessage, len(rows))
	for index, row := range rows {
		value := row.event.Metadata
		if value == nil {
			value = map[string]any{}
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		metadata[index] = raw
	}
	return metadata, nil
}

// sampleFact is the flattened per-row value block every candidate binds.
type sampleFact struct {
	sampleType    string
	sampleUUID    string
	start         time.Time
	end           time.Time
	value         *float64
	category      *int32
	unit          *string
	source        string
	hash          []byte
	metadata      string
	associationID string
}

// sampleFacts flattens the rows once so every candidate binds identical values.
func sampleFacts(rows []sampleRow) ([]sampleFact, error) {
	metadata, err := sampleMetadata(rows)
	if err != nil {
		return nil, err
	}
	facts := make([]sampleFact, len(rows))
	for index, row := range rows {
		event := row.event
		facts[index] = sampleFact{
			sampleType: event.Type,
			sampleUUID: event.UUID,
			start:      time.UnixMicro(*event.StartUs).UTC(),
			end:        time.UnixMicro(*event.EndUs).UTC(),
			source:     event.Source.BundleID,
			hash:       row.hash,
			metadata:   string(metadata[index]),
		}
		if event.Quantity != nil {
			value := event.Quantity.Value
			unit := event.Quantity.Unit
			facts[index].value = &value
			facts[index].unit = &unit
		}
		if event.Category != nil {
			coded := int32(*event.Category)
			facts[index].category = &coded
		}
	}
	return facts, nil
}

// SampleValuesStatement builds the multi-row VALUES insert of the baseline
// write strategy: one tuple per fact, eleven parameters per tuple, with $1 bound
// to the dataset.
func SampleValuesStatement(rows int) string {
	var sql strings.Builder
	sql.WriteString(`INSERT INTO health_samples(` + sampleColumns + `) VALUES `)
	for position := 0; position < rows; position++ {
		if position > 0 {
			sql.WriteByte(',')
		}
		base := 2 + position*11
		sql.WriteString("($1")
		for field := 0; field < 11; field++ {
			fmt.Fprintf(&sql, ",$%d", base+field)
		}
		sql.WriteByte(')')
	}
	return sql.String()
}

// sampleValuesArgs builds the bind values of SampleValuesStatement.
func sampleValuesArgs(dataset string, facts []sampleFact, rev int64) []any {
	args := make([]any, 0, 1+len(facts)*11)
	args = append(args, dataset)
	for _, fact := range facts {
		args = append(args, fact.sampleType, fact.sampleUUID, fact.start, fact.end,
			fact.value, fact.category, fact.unit, fact.source, fact.hash, rev, fact.metadata)
	}
	return args
}

// insertSampleRowsValues writes new facts with one multi-row statement per
// bounded batch. Each statement carries one parameter tuple per sample, so the
// statement count follows the batch count instead of the sample count. No
// ON CONFLICT clause appears here: a duplicate identity must still fail loudly
// instead of being silently skipped.
func insertSampleRowsValues(ctx context.Context, tx pgx.Tx, dataset string, rows []sampleRow, rev int64) error {
	facts, err := sampleFacts(rows)
	if err != nil {
		return err
	}
	for _, batch := range batchIndexes(len(facts), func(i int) int { return len(facts[i].metadata) + 96 }) {
		if _, err := tx.Exec(ctx, SampleValuesStatement(len(batch)), sampleValuesArgs(dataset, contiguous(facts, batch), rev)...); err != nil {
			return err
		}
	}
	return nil
}

// sampleArraysStatement binds the whole batch as ten fixed-shape array
// parameters. UUIDs travel as text[] and are cast per row because pgx has no
// guaranteed encode plan for []string into uuid[], and metadata travels as
// text[] cast to jsonb; timestamps, float8, int4 and bytea travel in their
// binary array form so precision and NULLs are preserved exactly.
const sampleArraysStatement = `INSERT INTO health_samples(` + sampleColumns + `)
SELECT $1::uuid, s.sample_type, s.sample_uuid::uuid, s.event_start, s.event_end,
       s.value_numeric, s.value_category, s.unit, s.source_bundle_id,
       s.content_sha256, $2::bigint, s.metadata::jsonb
FROM unnest($3::text[], $4::text[], $5::timestamptz[], $6::timestamptz[],
            $7::double precision[], $8::integer[], $9::text[], $10::text[],
            $11::bytea[], $12::text[])
     AS s(sample_type, sample_uuid, event_start, event_end, value_numeric,
          value_category, unit, source_bundle_id, content_sha256, metadata)`

// sampleArrayArgs builds the ten array parameters of sampleArraysStatement.
func sampleArrayArgs(dataset string, facts []sampleFact, rev int64) []any {
	count := len(facts)
	types := make([]string, count)
	uuids := make([]string, count)
	starts := make([]time.Time, count)
	ends := make([]time.Time, count)
	values := make([]*float64, count)
	categories := make([]*int32, count)
	units := make([]*string, count)
	sources := make([]string, count)
	hashes := make([][]byte, count)
	docs := make([]string, count)
	for position, fact := range facts {
		types[position] = fact.sampleType
		uuids[position] = fact.sampleUUID
		starts[position] = fact.start
		ends[position] = fact.end
		values[position] = fact.value
		categories[position] = fact.category
		units[position] = fact.unit
		sources[position] = fact.source
		hashes[position] = fact.hash
		docs[position] = fact.metadata
	}
	return []any{dataset, rev, types, uuids, starts, ends, values, categories, units, sources, hashes, docs}
}

// insertSampleRowsArrays runs one unnest() statement per bounded batch.
func insertSampleRowsArrays(ctx context.Context, tx pgx.Tx, dataset string, rows []sampleRow, rev int64) error {
	facts, err := sampleFacts(rows)
	if err != nil {
		return err
	}
	for _, batch := range batchIndexes(len(facts), func(i int) int { return len(facts[i].metadata) + 96 }) {
		if _, err := tx.Exec(ctx, sampleArraysStatement, sampleArrayArgs(dataset, contiguous(facts, batch), rev)...); err != nil {
			return err
		}
	}
	return nil
}

// AssociationValuesStatement builds the multi-row VALUES insert of the baseline
// association strategy.
func AssociationValuesStatement(rows int) string {
	var sql strings.Builder
	sql.WriteString(`INSERT INTO workout_samples(dataset_id,sample_type,sample_uuid,workout_uuid,association_id,source_bundle_id,commit_rev) VALUES `)
	for position := 0; position < rows; position++ {
		if position > 0 {
			sql.WriteByte(',')
		}
		base := 1 + position*7
		sql.WriteByte('(')
		for field := 0; field < 7; field++ {
			if field > 0 {
				sql.WriteByte(',')
			}
			fmt.Fprintf(&sql, "$%d", base+field)
		}
		sql.WriteByte(')')
	}
	return sql.String()
}

// associationValuesArgs builds the bind values of AssociationValuesStatement.
func associationValuesArgs(dataset, workoutID string, facts []sampleFact, rev int64) []any {
	args := make([]any, 0, len(facts)*7)
	for _, fact := range facts {
		args = append(args, dataset, fact.sampleType, fact.sampleUUID, workoutID, fact.associationID, fact.source, rev)
	}
	return args
}

// insertAssociationRowsValues writes the membership rows for samples that had no
// association yet. The association identity is generated here so each row stays
// immutable, and the statement is again one per bounded batch.
func insertAssociationRowsValues(ctx context.Context, tx pgx.Tx, dataset, workoutID string, rows []sampleRow, rev int64) error {
	facts, err := sampleFacts(rows)
	if err != nil {
		return err
	}
	for index := range facts {
		facts[index].associationID = syncing.NewID()
	}
	for _, batch := range batchIndexes(len(facts), func(i int) int { return len(facts[i].sampleUUID) + 96 }) {
		if _, err := tx.Exec(ctx, AssociationValuesStatement(len(batch)), associationValuesArgs(dataset, workoutID, contiguous(facts, batch), rev)...); err != nil {
			return err
		}
	}
	return nil
}

// associationArraysStatement binds the association batch as four array
// parameters.
const associationArraysStatement = `INSERT INTO workout_samples(dataset_id,sample_type,sample_uuid,workout_uuid,association_id,source_bundle_id,commit_rev)
SELECT $1::uuid, s.sample_type, s.sample_uuid::uuid, $2::uuid, s.association_id::uuid, s.source_bundle_id, $3::bigint
FROM unnest($4::text[], $5::text[], $6::text[], $7::text[])
     AS s(sample_type, sample_uuid, association_id, source_bundle_id)`

// associationArrayArgs builds the array parameters of associationArraysStatement.
func associationArrayArgs(dataset, workoutID string, facts []sampleFact, rev int64) []any {
	count := len(facts)
	types := make([]string, count)
	uuids := make([]string, count)
	associations := make([]string, count)
	sources := make([]string, count)
	for position, fact := range facts {
		types[position] = fact.sampleType
		uuids[position] = fact.sampleUUID
		associations[position] = fact.associationID
		sources[position] = fact.source
	}
	return []any{dataset, workoutID, rev, types, uuids, associations, sources}
}

// insertAssociationRowsArrays runs one unnest() association statement per batch.
func insertAssociationRowsArrays(ctx context.Context, tx pgx.Tx, dataset, workoutID string, rows []sampleRow, rev int64) error {
	facts, err := sampleFacts(rows)
	if err != nil {
		return err
	}
	for index := range facts {
		facts[index].associationID = syncing.NewID()
	}
	for _, batch := range batchIndexes(len(facts), func(i int) int { return len(facts[i].sampleUUID) + 96 }) {
		if _, err := tx.Exec(ctx, associationArraysStatement, associationArrayArgs(dataset, workoutID, contiguous(facts, batch), rev)...); err != nil {
			return err
		}
	}
	return nil
}

// sampleStaging is the fixed-shape transaction-local staging table. It carries
// the identity, the ordinal that preserves the caller's order, the core columns
// used for the set-based core comparison, and the content hash, so the
// projection can be verified with one join instead of a per-row round trip.
const sampleStaging = `CREATE TEMP TABLE IF NOT EXISTS rai_bundle_sample_staging(
    ordinal integer NOT NULL,
    sample_type text NOT NULL,
    sample_uuid uuid NOT NULL,
    event_start timestamptz NOT NULL,
    event_end timestamptz NOT NULL,
    value_numeric double precision,
    value_category integer,
    unit text,
    source_bundle_id text NOT NULL,
    content_sha256 bytea NOT NULL,
    metadata jsonb NOT NULL
) ON COMMIT DROP`

// associationStaging stages the association projection.
const associationStaging = `CREATE TEMP TABLE IF NOT EXISTS rai_bundle_association_staging(
    ordinal integer NOT NULL,
    sample_type text NOT NULL,
    sample_uuid uuid NOT NULL,
    association_id uuid NOT NULL,
    source_bundle_id text NOT NULL
) ON COMMIT DROP`

// sampleStagingColumns is the fixed staging column list.
var sampleStagingColumns = []string{"ordinal", "sample_type", "sample_uuid", "event_start", "event_end",
	"value_numeric", "value_category", "unit", "source_bundle_id", "content_sha256", "metadata"}

// associationStagingColumns is the fixed association staging column list.
var associationStagingColumns = []string{"ordinal", "sample_type", "sample_uuid", "association_id", "source_bundle_id"}

// sampleCopyProjectionStatement projects the staged samples into health_samples.
const sampleCopyProjectionStatement = `INSERT INTO health_samples(` + sampleColumns + `)
SELECT $1::uuid, s.sample_type, s.sample_uuid, s.event_start, s.event_end,
       s.value_numeric, s.value_category, s.unit, s.source_bundle_id,
       s.content_sha256, $2::bigint, s.metadata
FROM rai_bundle_sample_staging AS s
ORDER BY s.ordinal`

// associationCopyProjectionStatement projects the staged associations.
const associationCopyProjectionStatement = `INSERT INTO workout_samples(dataset_id,sample_type,sample_uuid,workout_uuid,association_id,source_bundle_id,commit_rev)
SELECT $1::uuid, s.sample_type, s.sample_uuid, $2::uuid, s.association_id, s.source_bundle_id, $3::bigint
FROM rai_bundle_association_staging AS s
ORDER BY s.ordinal`

// sampleCopyRecords builds the COPY records of the sample staging table.
func sampleCopyRecords(facts []sampleFact) [][]any {
	records := make([][]any, len(facts))
	for position, fact := range facts {
		records[position] = []any{
			int32(position), fact.sampleType, fact.sampleUUID, fact.start, fact.end,
			fact.value, fact.category, fact.unit, fact.source, fact.hash, fact.metadata,
		}
	}
	return records
}

// insertSampleRowsCopy streams the batch into the staging table with COPY and
// then projects it with a single set-based INSERT ... SELECT followed by a join
// barrier that fails when any staged row is missing from health_samples.
func insertSampleRowsCopy(ctx context.Context, tx pgx.Tx, dataset string, rows []sampleRow, rev int64) error {
	facts, err := sampleFacts(rows)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, sampleStaging); err != nil {
		return err
	}
	for _, batch := range batchIndexes(len(facts), func(i int) int { return len(facts[i].metadata) + 96 }) {
		if _, err := tx.Exec(ctx, `TRUNCATE rai_bundle_sample_staging`); err != nil {
			return err
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"rai_bundle_sample_staging"}, sampleStagingColumns,
			pgx.CopyFromRows(sampleCopyRecords(contiguous(facts, batch)))); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, sampleCopyProjectionStatement, dataset, rev); err != nil {
			return err
		}
		if err := verifySampleProjection(ctx, tx, dataset, rev); err != nil {
			return err
		}
	}
	return nil
}

// verifySampleProjection is the batch join barrier: every staged identity must
// be present in health_samples with the staged core values and content hash.
func verifySampleProjection(ctx context.Context, tx pgx.Tx, dataset string, rev int64) error {
	const statement = `SELECT count(*)
FROM rai_bundle_sample_staging AS s
LEFT JOIN health_samples AS h
       ON h.dataset_id = $1::uuid AND h.sample_type = s.sample_type AND h.sample_uuid = s.sample_uuid
WHERE h.sample_uuid IS NULL
   OR h.entity_revision <> $2::bigint
   OR h.event_start <> s.event_start
   OR h.event_end <> s.event_end
   OR h.unit IS DISTINCT FROM s.unit
   OR h.value_numeric IS DISTINCT FROM s.value_numeric
   OR h.value_category IS DISTINCT FROM s.value_category
   OR h.source_bundle_id <> s.source_bundle_id
   OR h.content_sha256 <> s.content_sha256`
	var missing int64
	if err := tx.QueryRow(ctx, statement, dataset, rev).Scan(&missing); err != nil {
		return err
	}
	if missing != 0 {
		return fmt.Errorf("staged sample projection is incomplete: %d rows missing or differing", missing)
	}
	return nil
}

// associationCopyRecords builds the COPY records of the association staging table.
func associationCopyRecords(facts []sampleFact) [][]any {
	records := make([][]any, len(facts))
	for position, fact := range facts {
		records[position] = []any{int32(position), fact.sampleType, fact.sampleUUID, fact.associationID, fact.source}
	}
	return records
}

// insertAssociationRowsCopy is the association counterpart of
// insertSampleRowsCopy: COPY into staging, one set-based projection, one join
// barrier.
func insertAssociationRowsCopy(ctx context.Context, tx pgx.Tx, dataset, workoutID string, rows []sampleRow, rev int64) error {
	facts, err := sampleFacts(rows)
	if err != nil {
		return err
	}
	for index := range facts {
		facts[index].associationID = syncing.NewID()
	}
	if _, err := tx.Exec(ctx, associationStaging); err != nil {
		return err
	}
	for _, batch := range batchIndexes(len(facts), func(i int) int { return len(facts[i].sampleUUID) + 96 }) {
		if _, err := tx.Exec(ctx, `TRUNCATE rai_bundle_association_staging`); err != nil {
			return err
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"rai_bundle_association_staging"}, associationStagingColumns,
			pgx.CopyFromRows(associationCopyRecords(contiguous(facts, batch)))); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, associationCopyProjectionStatement, dataset, workoutID, rev); err != nil {
			return err
		}
		const barrier = `SELECT count(*)
FROM rai_bundle_association_staging AS s
LEFT JOIN workout_samples AS a
       ON a.dataset_id = $1::uuid AND a.sample_type = s.sample_type AND a.sample_uuid = s.sample_uuid
WHERE a.sample_uuid IS NULL
   OR a.workout_uuid <> $2::uuid
   OR a.association_id <> s.association_id
   OR a.commit_rev <> $3::bigint`
		var missing int64
		if err := tx.QueryRow(ctx, barrier, dataset, workoutID, rev).Scan(&missing); err != nil {
			return err
		}
		if missing != 0 {
			return fmt.Errorf("staged association projection is incomplete: %d rows missing or differing", missing)
		}
	}
	return nil
}

// ExplainFactWrites captures EXPLAIN (ANALYZE, BUFFERS, WAL) for every write
// candidate over the same fact batch, each inside its own savepoint that is
// rolled back afterwards so the candidates cannot collide. It exists for the R5
// write-path evidence; the caller must roll its transaction back.
func ExplainFactWrites(ctx context.Context, tx pgx.Tx, dataset, workoutID string, events []syncing.Event, rev int64) (map[string][]string, error) {
	rows := make([]sampleRow, len(events))
	for index, event := range events {
		canonical, hash := event.Canonical()
		rows[index] = sampleRow{event: event, canonical: canonical, hash: hash}
	}
	facts, err := sampleFacts(rows)
	if err != nil {
		return nil, err
	}
	associations := make([]sampleFact, len(facts))
	copy(associations, facts)
	for index := range associations {
		associations[index].associationID = syncing.NewID()
	}
	plans := make(map[string][]string, 3)
	for _, strategy := range []WriteStrategy{StrategyValues, StrategyArrays, StrategyCopy} {
		if _, err := tx.Exec(ctx, `SAVEPOINT explain_candidate`); err != nil {
			return nil, err
		}
		lines, runErr := explainCandidate(ctx, tx, strategy, dataset, workoutID, facts, associations, rev)
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT explain_candidate`); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `RELEASE SAVEPOINT explain_candidate`); err != nil {
			return nil, err
		}
		if runErr != nil {
			return nil, runErr
		}
		plans[string(strategy)] = lines
	}
	return plans, nil
}

// explainCandidate runs the sample and association statement of one strategy
// under EXPLAIN ANALYZE and returns the plan text.
func explainCandidate(ctx context.Context, tx pgx.Tx, strategy WriteStrategy, dataset, workoutID string, facts, associations []sampleFact, rev int64) ([]string, error) {
	queries := make([][]any, 0, 2)
	switch strategy {
	case StrategyArrays:
		queries = append(queries,
			[]any{sampleArraysStatement, sampleArrayArgs(dataset, facts, rev)},
			[]any{associationArraysStatement, associationArrayArgs(dataset, workoutID, associations, rev)})
	case StrategyCopy:
		if _, err := tx.Exec(ctx, sampleStaging); err != nil {
			return nil, err
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"rai_bundle_sample_staging"}, sampleStagingColumns,
			pgx.CopyFromRows(sampleCopyRecords(facts))); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, associationStaging); err != nil {
			return nil, err
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"rai_bundle_association_staging"}, associationStagingColumns,
			pgx.CopyFromRows(associationCopyRecords(associations))); err != nil {
			return nil, err
		}
		queries = append(queries,
			[]any{sampleCopyProjectionStatement, []any{dataset, rev}},
			[]any{associationCopyProjectionStatement, []any{dataset, workoutID, rev}})
	default:
		queries = append(queries,
			[]any{SampleValuesStatement(len(facts)), sampleValuesArgs(dataset, facts, rev)},
			[]any{AssociationValuesStatement(len(associations)), associationValuesArgs(dataset, workoutID, associations, rev)})
	}
	lines := make([]string, 0, 64)
	for _, query := range queries {
		statement := "EXPLAIN (ANALYZE, BUFFERS, WAL) " + query[0].(string)
		args := query[1].([]any)
		result, err := tx.Query(ctx, statement, args...)
		if err != nil {
			return nil, err
		}
		for result.Next() {
			var line string
			if err = result.Scan(&line); err != nil {
				result.Close()
				return nil, err
			}
			lines = append(lines, line)
		}
		result.Close()
		if err = result.Err(); err != nil {
			return nil, err
		}
	}
	return lines, nil
}
