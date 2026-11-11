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
