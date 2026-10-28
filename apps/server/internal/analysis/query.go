package analysis

import "time"

// Current is an API contract, separate from storage/dbgen row models.
type Current struct {
	Scope            string    `json:"scopeKey"`
	RunID            string    `json:"runID"`
	Generation       int64     `json:"generation"`
	Algorithm        string    `json:"algorithm"`
	AlgorithmVersion string    `json:"algorithmVersion"`
	InputVersion     string    `json:"inputVersion"`
	InputHash        string    `json:"inputHash"`
	PublishedAt      time.Time `json:"publishedAt"`
	ComputedAt       time.Time `json:"computedAt"`
	Metrics          []Metric  `json:"metrics"`
}
