// Package product defines read-only native product DTOs. It owns no algorithms.
package product

import "rai/server/internal/analysis"

type Workout struct {
	ID             string            `json:"workoutID"`
	StartUs        int64             `json:"startUs"`
	ActivityType   int64             `json:"activityType"`
	DurationS      float64           `json:"durationS"`
	DistanceM      *float64          `json:"distanceM"`
	AnalysisStatus string            `json:"analysisStatus"`
	Metrics        []analysis.Metric `json:"metrics"`
	RouteStatus    string            `json:"routeStatus"`
}
type Page struct {
	Items []Workout `json:"items"`
	Next  string    `json:"next"`
}
type Day struct {
	Date      string   `json:"date"`
	Count     int      `json:"count"`
	DistanceM *float64 `json:"distanceM"`
	Load      *float64 `json:"load"`
}
type Overview struct {
	Timezone string `json:"timezone"`
	Today    string `json:"today"`
	Days     []Day  `json:"days"`
}
type History struct {
	Metrics []analysis.Metric `json:"metrics"`
}
