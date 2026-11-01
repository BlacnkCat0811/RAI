-- +goose Up
-- Route timestamps are stored as timestamptz microseconds. PostgreSQL's
-- interval multiplication rounds the final legal microsecond of year 9999 up
-- to year 10000, which made the database reject otherwise valid source points.
-- Keep the same function and signature, but compare the stored envelope using
-- numeric epoch microseconds so every legal year 1..9999 value round-trips.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_route_append_chunk(
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
       (min_stamp < (extract(epoch FROM r.first_recorded_at) * 1000000)::numeric OR
        max_stamp > (extract(epoch FROM r.last_recorded_at) * 1000000)::numeric) THEN
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

-- No Down: the function must continue accepting exact legal source timestamps
-- after a code rollback; restoring the old rounding bug would reject facts.

-- The projection trigger is the second conversion point. Splitting whole
-- seconds from the sub-second remainder avoids interval multiplication's
-- year-9999 rounding while preserving the original microsecond exactly.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_project_route_chunk() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
BEGIN
    INSERT INTO public.route_points(
        dataset_id, route_uuid, chunk_index, sequence, recorded_at,
        latitude, longitude, altitude, horizontal_accuracy, vertical_accuracy,
        speed, course)
    SELECT
        NEW.dataset_id, NEW.route_uuid, NEW.chunk_index,
        (p->>'sequence')::bigint,
        TIMESTAMPTZ 'epoch'
            + ((p->>'recordedAtUs')::bigint / 1000000) * INTERVAL '1 second'
            + ((p->>'recordedAtUs')::bigint % 1000000) * INTERVAL '1 microsecond',
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
