package postgres

import (
	"encoding/json"

	"rai/server/internal/syncing"
)

type workoutSampleIdentity struct {
	Type string `json:"type"`
	UUID string `json:"uuid"`
}

type workoutRouteIdentity struct {
	RouteID string `json:"routeID"`
}

// This is an internal read model, never an upload payload. The authoritative
// protocol-6 bytes (and their digest) remain in workout_ingests.raw_json. Only
// identities actually projected into facts and their shared source belong in
// JSONB, so arbitrary extensions cannot prevent the fact transaction committing.
type workoutImportProjection struct {
	ProtocolVersion   int                     `json:"protocolVersion"`
	ProjectionVersion int                     `json:"projectionVersion"`
	WorkoutSource     syncing.HealthKitSource `json:"workoutSource"`
	Samples           []workoutSampleIdentity `json:"samples"`
	Routes            []workoutRouteIdentity  `json:"routes"`
}

func workoutImportJSON(bundle syncing.WorkoutBundle, raw []byte) ([]byte, error) {
	if bundle.ProtocolVersion != 6 {
		return raw, nil
	}
	if bundle.SourceTable == nil {
		return nil, syncing.Fail(422, "unsupported_payload")
	}
	p := workoutImportProjection{ProtocolVersion: 6, ProjectionVersion: 1,
		WorkoutSource: bundle.SourceTable.Workout,
		Samples:       make([]workoutSampleIdentity, 0, len(bundle.Samples)),
		Routes:        make([]workoutRouteIdentity, 0, len(bundle.Routes))}
	for _, sample := range bundle.Samples {
		p.Samples = append(p.Samples, workoutSampleIdentity{Type: sample.Type, UUID: sample.UUID})
	}
	for _, route := range bundle.Routes {
		p.Routes = append(p.Routes, workoutRouteIdentity{RouteID: route.RouteID})
	}
	return json.Marshal(p)
}

func workoutImportSources(raw []byte) (*syncing.BundleSourceTable, error) {
	var header struct {
		ProjectionVersion json.RawMessage `json:"projectionVersion"`
		Source            json.RawMessage `json:"source"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return nil, err
	}
	// Valid wire payloads always have a root source. Do not interpret a producer's
	// unknown projectionVersion/workoutSource extensions as our internal format.
	if len(header.Source) != 0 {
		return syncing.WorkoutPayloadSourceTable(raw)
	}
	var version int
	if json.Unmarshal(header.ProjectionVersion, &version) != nil || version != 1 {
		return nil, syncing.Fail(422, "unsupported_payload")
	}
	var p workoutImportProjection
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.ProtocolVersion != 6 || p.WorkoutSource.Validate() != nil {
		return nil, syncing.Fail(422, "unsupported_payload")
	}
	table := &syncing.BundleSourceTable{Workout: p.WorkoutSource,
		Samples: make(map[string]syncing.HealthKitSource, len(p.Samples)),
		Routes:  make(map[string]syncing.HealthKitSource, len(p.Routes))}
	for _, sample := range p.Samples {
		table.Samples[sample.Type+":"+sample.UUID] = p.WorkoutSource
	}
	for _, route := range p.Routes {
		table.Routes[route.RouteID] = p.WorkoutSource
	}
	return table, nil
}
