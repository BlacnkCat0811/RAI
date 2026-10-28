package analysis

// All intervals use absolute Unix microseconds. Only input loaders choose facts.
type Parameters struct {
	Version    int64   `json:"version"`
	RestHR     float64 `json:"restingHR"`
	MaxHR      float64 `json:"maxHR"`
	A          float64 `json:"coefficientA"`
	B          float64 `json:"coefficientB"`
	Provenance string  `json:"provenance"`
}
type Observation struct {
	ID    string  `json:"id"`
	Type  string  `json:"type"`
	Start int64   `json:"startUs"`
	End   int64   `json:"endUs"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}
type Statistic struct {
	Type    string   `json:"type"`
	Average *float64 `json:"average"`
	Maximum *float64 `json:"maximum"`
}
type Point struct {
	Route              string   `json:"route"`
	At                 int64    `json:"atUs"`
	Latitude           float64  `json:"latitude"`
	Longitude          float64  `json:"longitude"`
	Altitude           *float64 `json:"altitude"`
	HorizontalAccuracy *float64 `json:"horizontalAccuracy"`
	VerticalAccuracy   *float64 `json:"verticalAccuracy"`
}
type WorkoutInput struct {
	ID           string        `json:"id"`
	Start        int64         `json:"startUs"`
	End          int64         `json:"endUs"`
	Duration     float64       `json:"durationSeconds"`
	Distance     *float64      `json:"distanceMeters"`
	Deleted      bool          `json:"deleted"`
	Parameters   *Parameters   `json:"parameters"`
	Observations []Observation `json:"observations"`
	Statistics   []Statistic   `json:"statistics"`
	Route        []Point       `json:"route"`
	Lineage      []string      `json:"lineage"`
}
type Load struct {
	WorkoutID string   `json:"workoutID"`
	RunID     string   `json:"runID"`
	Day       string   `json:"day"`
	Value     *float64 `json:"value"`
}
type StateInput struct {
	From     string `json:"from"`
	Through  string `json:"through"`
	Timezone string `json:"timezone"`
	Loads    []Load `json:"loads"`
}
