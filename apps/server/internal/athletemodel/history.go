package athletemodel

import "errors"

type HistoryPoint struct {
	Date           string   `json:"date"`
	Metric         string   `json:"metric"`
	State          string   `json:"state"`
	Value          *float64 `json:"value"`
	Unit           string   `json:"unit"`
	ModelVersion   string   `json:"modelVersion"`
	PayloadVersion string   `json:"payloadVersion"`
	ConfigHash     string   `json:"configHash,omitempty"`
	Mode           string   `json:"mode,omitempty"`
	Source         string   `json:"source,omitempty"`
	EvidenceAt     *int64   `json:"evidenceAt,omitempty"`
	AsOf           int64    `json:"asOf"`
	ReasonCodes    []string `json:"reasonCodes"`
}

// HistoryTrend is a descriptive difference between two real published points
// of the same metric, model version and payload version. It is not a fitted
// slope and is never synthesized when a point is missing.
type HistoryTrend struct {
	Kind        string   `json:"kind"`
	FromDate    string   `json:"fromDate"`
	ToDate      string   `json:"toDate"`
	Days        int      `json:"days"`
	FromValue   *float64 `json:"fromValue"`
	ToValue     *float64 `json:"toValue"`
	Delta       *float64 `json:"delta"`
	Unit        string   `json:"unit"`
	ReasonCodes []string `json:"reasonCodes"`
}

type HistoryPage struct {
	Metric      string         `json:"metric"`
	View        string         `json:"view"`
	Points      []HistoryPoint `json:"points"`
	Trend       *HistoryTrend  `json:"trend,omitempty"`
	NextCursor  *string        `json:"nextCursor"`
	Assignment  Assignment     `json:"assignment"`
	ManifestSHA string         `json:"manifestSHA256,omitempty"`
}

// ErrHistoryCursor is returned when an opaque history cursor belongs to a
// different dataset, metric, assignment revision, view, or range. Callers must
// restart the query instead of silently mixing revisions.
var ErrHistoryCursor = errors.New("athlete_history_cursor_mismatch")
var ErrHistoricalInputUnavailable = errors.New("historical_input_unavailable")
