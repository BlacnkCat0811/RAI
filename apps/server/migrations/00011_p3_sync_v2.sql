-- +goose Up
-- Phase 3 completes route publication, late sample/Workout association, and
-- append-only Workout enrichment. Existing P2 staging rows remain readable but
-- are never treated as published routes.

ALTER TABLE workout_routes
    ADD COLUMN dataset_epoch uuid,
    ADD COLUMN collector_epoch uuid,
    ADD COLUMN expected_chunk_count integer NOT NULL DEFAULT 1
        CHECK (expected_chunk_count BETWEEN 1 AND 1000),
    ADD COLUMN expected_point_count bigint NOT NULL DEFAULT 1
        CHECK (expected_point_count BETWEEN 1 AND 1000000),
    ADD COLUMN first_recorded_at timestamptz,
    ADD COLUMN last_recorded_at timestamptz,
    ADD COLUMN committed_chunks integer NOT NULL DEFAULT 0
        CHECK (committed_chunks BETWEEN 0 AND 1000),
    ADD COLUMN committed_points bigint NOT NULL DEFAULT 0
        CHECK (committed_points BETWEEN 0 AND 1000000),
    ADD COLUMN manifest_sha256 bytea NOT NULL DEFAULT decode(repeat('00',32),'hex')
        CHECK (octet_length(manifest_sha256) = 32),
    ADD COLUMN payload_manifest_sha256 bytea NOT NULL DEFAULT decode(repeat('00',32),'hex')
        CHECK (octet_length(payload_manifest_sha256) = 32),
    ADD COLUMN expected_payload_manifest_sha256 bytea
        CHECK (expected_payload_manifest_sha256 IS NULL OR octet_length(expected_payload_manifest_sha256) = 32),
    ADD COLUMN sealed_rev bigint CHECK (sealed_rev IS NULL OR sealed_rev > 0);

ALTER TABLE workout_routes
    ADD CONSTRAINT workout_routes_time_order
        CHECK (first_recorded_at IS NULL OR last_recorded_at IS NULL OR first_recorded_at <= last_recorded_at),
    ADD CONSTRAINT workout_routes_counts
        CHECK (committed_chunks <= expected_chunk_count AND committed_points <= expected_point_count),
    ADD CONSTRAINT workout_routes_sealed_rev
        CHECK (state <> 'sealed' OR sealed_rev IS NOT NULL);

ALTER TABLE workout_routes
    ADD COLUMN chunk_count integer CHECK (chunk_count BETWEEN 1 AND 1000),
    ADD COLUMN total_points bigint CHECK (total_points BETWEEN 1 AND 1000000);
ALTER TABLE workout_routes
    ADD CONSTRAINT workout_routes_seal_counts
        CHECK ((state = 'sealed') = (chunk_count IS NOT NULL AND total_points IS NOT NULL)),
    ADD CONSTRAINT workout_routes_seal_counts_match
        CHECK (chunk_count IS NULL OR (chunk_count = expected_chunk_count AND total_points = expected_point_count));

-- Canonical chunk JSON remains the source of truth. These columns make retry
-- identity and hash-chain verification independent of the projection table.
ALTER TABLE route_chunks
    ADD COLUMN wire_sha256 bytea NOT NULL DEFAULT decode(repeat('00',32),'hex')
        CHECK (octet_length(wire_sha256) = 32),
    ADD COLUMN manifest_sha256 bytea NOT NULL DEFAULT decode(repeat('00',32),'hex')
        CHECK (octet_length(manifest_sha256) = 32),
    ADD COLUMN payload_manifest_sha256 bytea NOT NULL DEFAULT decode(repeat('00',32),'hex')
        CHECK (octet_length(payload_manifest_sha256) = 32),
    ADD COLUMN receipt bytea;
ALTER TABLE route_chunks
    ADD CONSTRAINT route_chunks_point_limit CHECK (point_count BETWEEN 1 AND 5000),
    ADD CONSTRAINT route_chunks_index_limit CHECK (chunk_index BETWEEN 0 AND 999);

-- One sample owns at most one Workout. New associations carry their own
-- idempotency key and original receipt while staying in this one table.
ALTER TABLE workout_samples
    ADD COLUMN association_id uuid,
    ADD COLUMN source_bundle_id text,
    ADD COLUMN association_receipt bytea,
    ADD COLUMN commit_rev bigint CHECK (commit_rev IS NULL OR commit_rev > 0),
    ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();
CREATE UNIQUE INDEX workout_samples_association_id
    ON workout_samples(dataset_id, association_id) WHERE association_id IS NOT NULL;

-- Append-only enrichment facts. The Workout row and its original payload remain
-- immutable; consumers select the highest observedAt/sourceVersion view.
CREATE TABLE workout_enrichments (
    dataset_id uuid NOT NULL REFERENCES datasets(id),
    enrichment_id uuid NOT NULL,
    workout_uuid uuid NOT NULL,
    source_bundle_id text NOT NULL CHECK (source_bundle_id <> ''),
    source_version bigint NOT NULL CHECK (source_version > 0),
    observed_at_us bigint NOT NULL CHECK (observed_at_us BETWEEN -62135596800000000 AND 253402300799999999),
    total_distance_m double precision
        CHECK (total_distance_m IS NULL OR (total_distance_m >= 0 AND total_distance_m < 'Infinity'::float8)),
    total_energy_kcal double precision
        CHECK (total_energy_kcal IS NULL OR (total_energy_kcal >= 0 AND total_energy_kcal < 'Infinity'::float8)),
    metadata jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(metadata) = 'object'),
    route_available boolean,
    content_sha256 bytea NOT NULL CHECK (octet_length(content_sha256) = 32),
    receipt bytea NOT NULL,
    commit_rev bigint NOT NULL CHECK (commit_rev > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (dataset_id, enrichment_id),
    UNIQUE (dataset_id, workout_uuid, source_bundle_id, source_version),
    FOREIGN KEY (dataset_id, workout_uuid)
        REFERENCES workouts(dataset_id, workout_uuid) ON DELETE CASCADE,
    CHECK (total_distance_m IS NOT NULL OR total_energy_kcal IS NOT NULL OR metadata <> '{}' OR route_available IS NOT NULL)
);
CREATE INDEX workout_enrichments_workout
    ON workout_enrichments(dataset_id, workout_uuid, observed_at_us DESC, source_version DESC);

-- Durable deletion receipts are separate from the tombstone barrier so an
-- exact retry can recover the original response after the route row is gone.
CREATE TABLE route_deletions (
    dataset_id uuid NOT NULL REFERENCES datasets(id),
    route_uuid uuid NOT NULL,
    workout_uuid uuid,
    deleted_rev bigint NOT NULL CHECK (deleted_rev >= 0),
    reason text NOT NULL,
    receipt bytea NOT NULL,
    deleted_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (dataset_id, route_uuid)
);

-- The projection is derived from the canonical chunk JSON in the database, not
-- supplied by the API role. This closes the P2 gap where only the Go path kept
-- route_chunks.points and route_points in sync.
-- +goose StatementBegin
CREATE FUNCTION rai_project_route_chunk() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
BEGIN
    INSERT INTO public.route_points(
        dataset_id, route_uuid, chunk_index, sequence, recorded_at,
        latitude, longitude, altitude, horizontal_accuracy, vertical_accuracy,
        speed, course)
    SELECT
        NEW.dataset_id, NEW.route_uuid, NEW.chunk_index,
        (p->>'sequence')::bigint,
        TIMESTAMPTZ 'epoch' + ((p->>'recordedAtUs')::bigint) * INTERVAL '1 microsecond',
        (p->>'latitude')::double precision,
        (p->>'longitude')::double precision,
        (p->>'altitude')::double precision,
        (p->>'horizontalAccuracy')::double precision,
        (p->>'verticalAccuracy')::double precision,
        (p->>'speed')::double precision,
        (p->>'course')::double precision
    FROM jsonb_array_elements(NEW.points) AS elem(p);
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER route_chunk_project
    AFTER INSERT ON route_chunks
    FOR EACH ROW EXECUTE FUNCTION rai_project_route_chunk();

-- The only chunk write path: lock the route, enforce continuity/counts/hashes,
-- insert the immutable chunk (trigger projects points) and advance both chains.
-- +goose StatementBegin
CREATE FUNCTION rai_route_append_chunk(
    p_dataset uuid,
    p_route uuid,
    p_index integer,
    p_point_count integer,
    p_wire_sha256 bytea,
    p_points_sha256 bytea,
    p_points jsonb
) RETURNS bytea
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE
    r public.workout_routes%ROWTYPE;
    existing public.route_chunks%ROWTYPE;
    min_sequence bigint;
    max_sequence bigint;
    distinct_sequences bigint;
    min_stamp bigint;
    max_stamp bigint;
    point_count_in_chunk bigint;
    new_manifest bytea;
    new_payload_manifest bytea;
    receipt_body bytea;
BEGIN
    SELECT * INTO r FROM public.workout_routes
    WHERE dataset_id=p_dataset AND route_uuid=p_route FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'route_not_found' USING ERRCODE='RA001';
    END IF;

    SELECT * INTO existing FROM public.route_chunks
    WHERE dataset_id=p_dataset AND route_uuid=p_route AND chunk_index=p_index;
    IF FOUND THEN
        IF existing.wire_sha256 = p_wire_sha256 THEN
            RETURN existing.receipt;
        END IF;
        RAISE EXCEPTION 'route_chunk_conflict' USING ERRCODE='RA013';
    END IF;

    IF r.state <> 'pending' THEN
        RAISE EXCEPTION 'route_already_sealed' USING ERRCODE='RA023';
    END IF;
    IF p_index <> r.committed_chunks THEN
        RAISE EXCEPTION 'route_chunk_gap' USING ERRCODE='RA011';
    END IF;
    IF p_point_count < 1 OR p_point_count > 5000 OR
       jsonb_typeof(p_points) <> 'array' OR jsonb_array_length(p_points) <> p_point_count THEN
        RAISE EXCEPTION 'route_chunk_too_large' USING ERRCODE='RA012';
    END IF;
    IF octet_length(p_wire_sha256) <> 32 OR octet_length(p_points_sha256) <> 32 THEN
        RAISE EXCEPTION 'unsupported_payload' USING ERRCODE='RA014';
    END IF;
    IF r.committed_chunks >= r.expected_chunk_count OR
       r.committed_points + p_point_count > r.expected_point_count THEN
        RAISE EXCEPTION 'route_too_large' USING ERRCODE='RA015';
    END IF;

    SELECT count(*)::bigint, min((p->>'sequence')::bigint), max((p->>'sequence')::bigint),
           count(DISTINCT (p->>'sequence')::bigint), min((p->>'recordedAtUs')::bigint),
           max((p->>'recordedAtUs')::bigint)
    INTO point_count_in_chunk, min_sequence, max_sequence, distinct_sequences, min_stamp, max_stamp
    FROM jsonb_array_elements(p_points) AS elem(p);
    IF point_count_in_chunk <> p_point_count OR distinct_sequences <> p_point_count OR min_sequence <> r.committed_points OR
       max_sequence <> r.committed_points + p_point_count - 1 THEN
        RAISE EXCEPTION 'route_sequence_invalid' USING ERRCODE='RA016';
    END IF;
    IF r.first_recorded_at IS NOT NULL AND
       (TIMESTAMPTZ 'epoch' + min_stamp * INTERVAL '1 microsecond' < r.first_recorded_at OR
        TIMESTAMPTZ 'epoch' + max_stamp * INTERVAL '1 microsecond' > r.last_recorded_at) THEN
        RAISE EXCEPTION 'route_time_invalid' USING ERRCODE='RA017';
    END IF;

    new_manifest := sha256(r.manifest_sha256 || p_points_sha256);
    new_payload_manifest := sha256(r.payload_manifest_sha256 || p_wire_sha256);
    receipt_body := convert_to(jsonb_build_object(
        'routeID', p_route,
        'chunkIndex', p_index,
        'ingestStatus', 'pending',
        'pointCount', p_point_count,
        'payloadSHA256', encode(p_wire_sha256, 'hex'),
        'chunkSHA256', encode(p_points_sha256, 'hex'),
        'manifestSHA256', encode(new_manifest, 'hex'),
        'payloadManifestSHA256', encode(new_payload_manifest, 'hex'),
        'committedChunks', r.committed_chunks + 1,
        'totalPoints', r.committed_points + p_point_count
    )::text, 'UTF8');

    INSERT INTO public.route_chunks(
        dataset_id, route_uuid, chunk_index, point_count, content_sha256, points,
        wire_sha256, manifest_sha256, payload_manifest_sha256, receipt)
    VALUES (
        p_dataset, p_route, p_index, p_point_count, p_points_sha256, p_points,
        p_wire_sha256, new_manifest, new_payload_manifest, receipt_body);

    UPDATE public.workout_routes
    SET committed_chunks=committed_chunks+1,
        committed_points=committed_points+p_point_count,
        manifest_sha256=new_manifest,
        payload_manifest_sha256=new_payload_manifest
    WHERE dataset_id=p_dataset AND route_uuid=p_route;

    RETURN receipt_body;
END $$;
-- +goose StatementEnd

-- Seal is the sole publish transition. It verifies indices, point continuity,
-- counts and both digest chains before writing state. It returns applied=false
-- for an exact replay of an already sealed route.
-- +goose StatementBegin
CREATE FUNCTION rai_route_seal(
    p_dataset uuid,
    p_route uuid,
    p_chunk_count integer,
    p_total_points bigint,
    p_manifest_sha256 bytea,
    p_payload_manifest_sha256 bytea,
    p_receipt bytea,
    p_revision bigint
) RETURNS TABLE(applied boolean, receipt bytea)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE
    r public.workout_routes%ROWTYPE;
    actual_chunks integer;
    actual_points bigint;
    min_index integer;
    max_index integer;
    point_rows bigint;
    min_sequence bigint;
    max_sequence bigint;
    distinct_sequences bigint;
BEGIN
    SELECT * INTO r FROM public.workout_routes
    WHERE dataset_id=p_dataset AND route_uuid=p_route FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'route_not_found' USING ERRCODE='RA001';
    END IF;
    IF r.state = 'sealed' THEN
        IF r.chunk_count = p_chunk_count AND r.total_points = p_total_points AND
           r.manifest_sha256 = p_manifest_sha256 AND
           r.payload_manifest_sha256 = p_payload_manifest_sha256 THEN
            applied := false;
            receipt := r.receipt;
            RETURN NEXT;
            RETURN;
        END IF;
        RAISE EXCEPTION 'route_already_sealed' USING ERRCODE='RA023';
    END IF;
    IF p_chunk_count <> r.expected_chunk_count OR p_total_points <> r.expected_point_count OR
       p_chunk_count <> r.committed_chunks OR p_total_points <> r.committed_points THEN
        RAISE EXCEPTION 'route_incomplete' USING ERRCODE='RA021';
    END IF;
    IF r.manifest_sha256 <> p_manifest_sha256 OR
       r.payload_manifest_sha256 <> p_payload_manifest_sha256 OR
       (r.expected_payload_manifest_sha256 IS NOT NULL AND
        r.expected_payload_manifest_sha256 <> p_payload_manifest_sha256) THEN
        RAISE EXCEPTION 'route_manifest_mismatch' USING ERRCODE='RA022';
    END IF;

    SELECT count(*)::integer, coalesce(sum(point_count),0)::bigint,
           min(chunk_index), max(chunk_index)
    INTO actual_chunks, actual_points, min_index, max_index
    FROM public.route_chunks WHERE dataset_id=p_dataset AND route_uuid=p_route;
    IF actual_chunks <> p_chunk_count OR actual_points <> p_total_points OR
       min_index <> 0 OR max_index <> p_chunk_count - 1 THEN
        RAISE EXCEPTION 'route_incomplete' USING ERRCODE='RA021';
    END IF;

    SELECT count(*)::bigint, min(sequence), max(sequence), count(DISTINCT sequence)
    INTO point_rows, min_sequence, max_sequence, distinct_sequences
    FROM public.route_points WHERE dataset_id=p_dataset AND route_uuid=p_route;
    IF point_rows <> p_total_points OR distinct_sequences <> p_total_points OR
       min_sequence <> 0 OR max_sequence <> p_total_points - 1 THEN
        RAISE EXCEPTION 'route_incomplete' USING ERRCODE='RA021';
    END IF;

    UPDATE public.workout_routes
    SET state='sealed', sealed_at=now(), receipt=p_receipt, sealed_rev=p_revision,
        chunk_count=p_chunk_count, total_points=p_total_points
    WHERE dataset_id=p_dataset AND route_uuid=p_route AND state='pending';

    applied := true;
    receipt := p_receipt;
    RETURN NEXT;
END $$;
-- +goose StatementEnd

-- Deletion and tombstone are one statement path. The API role cannot delete a
-- route directly, so stale identity cannot be made registrable again by
-- bypassing the barrier.
-- +goose StatementBegin
CREATE FUNCTION rai_route_delete(
    p_dataset uuid,
    p_route uuid,
    p_revision bigint,
    p_reason text
) RETURNS TABLE(workout_uuid uuid, route_existed boolean, tombstone_created boolean)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE
    old_workout uuid;
    existed boolean;
BEGIN
    SELECT r.workout_uuid INTO old_workout
    FROM public.workout_routes r
    WHERE r.dataset_id=p_dataset AND r.route_uuid=p_route FOR UPDATE;
    existed := FOUND;

    INSERT INTO public.entity_tombstones(
        dataset_id, sample_type, sample_uuid, deleted_rev, reason)
    VALUES (p_dataset, 'HKWorkoutRouteTypeIdentifier', p_route, p_revision, p_reason)
    ON CONFLICT (dataset_id, sample_type, sample_uuid) DO NOTHING;
    tombstone_created := FOUND;

    IF existed THEN
        DELETE FROM public.workout_routes WHERE dataset_id=p_dataset AND route_uuid=p_route;
    END IF;

    workout_uuid := old_workout;
    route_existed := existed;
    RETURN NEXT;
END $$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION rai_project_route_chunk() FROM PUBLIC;
REVOKE ALL ON FUNCTION rai_route_append_chunk(uuid,uuid,integer,integer,bytea,bytea,jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION rai_route_seal(uuid,uuid,integer,bigint,bytea,bytea,bytea,bigint) FROM PUBLIC;
REVOKE ALL ON FUNCTION rai_route_delete(uuid,uuid,bigint,text) FROM PUBLIC;

-- No Down: published route facts, tombstones and receipts are not rollback data.
