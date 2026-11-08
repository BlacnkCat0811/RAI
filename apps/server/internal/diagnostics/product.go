package diagnostics

// ProductSchema is the stable read-only projection of research diagnostics.
// The projection exposes display values only; evidence arrays, source epochs
// and raw samples never cross this contract.
const ProductSchema = "rai.athlete_diagnostics/1"

// ProductStatus normalises the two research lifecycles (diagnostics.Status and
// the performance result status) into one product vocabulary.
type ProductStatus string

const (
	ProductAvailable    ProductStatus = "available"
	ProductPartial      ProductStatus = "partial"
	ProductCollecting   ProductStatus = "collecting"
	ProductInsufficient ProductStatus = "insufficient"
	ProductUnstable     ProductStatus = "unstable"
	ProductStale        ProductStatus = "stale"
	ProductUnavailable  ProductStatus = "unavailable"
)

// ProductReasonTechnicalFailure is used when a persisted artifact fails its
// schema, identity or content-hash validation at read time.
const ProductReasonTechnicalFailure ReasonCode = "technical_failure"

// ProductAssignment identifies the active model stack the diagnostics belong
// to. It mirrors the product snapshot assignment so clients can detect a stack
// switch between reads.
type ProductAssignment struct {
	Channel string `json:"channel"`
	StackID string `json:"stackID"`
	Version int64  `json:"version"`
}

// ProductItem is one research model result. At most one view is populated;
// the key selects which one. Nil views mean the model produced no value, not
// that the value was zero.
type ProductItem struct {
	Key           string                `json:"key"`
	Model         string                `json:"model"`
	Version       string                `json:"version"`
	Schema        string                `json:"schemaVersion"`
	Experimental  bool                  `json:"experimental"`
	Status        ProductStatus         `json:"status"`
	ReasonCodes   []ReasonCode          `json:"reasonCodes"`
	Support       Support               `json:"support"`
	AsOf          int64                 `json:"asOfUs"`
	PublishedAt   int64                 `json:"publishedAtUs"`
	Day           string                `json:"day"`
	Maximal       *MaximalProduct       `json:"maximal,omitempty"`
	Durability    *DurabilityProduct    `json:"durability,omitempty"`
	Efficiency    *EfficiencyProduct    `json:"efficiency,omitempty"`
	CriticalSpeed *CriticalSpeedProduct `json:"criticalSpeed,omitempty"`
	LoadSettling  *SettlingProduct      `json:"loadSettling,omitempty"`
}

type MaximalProduct struct {
	Score              *float64 `json:"score,omitempty"`
	V60                *float64 `json:"v60,omitempty"`
	Gamma              *float64 `json:"gamma,omitempty"`
	EnduranceRetention *float64 `json:"enduranceRetention,omitempty"`
	Confidence         float64  `json:"confidence"`
	AnchorDay          string   `json:"anchorDay,omitempty"`
}

type DurabilityProduct struct {
	WorkoutID       string   `json:"workoutID"`
	DurationSeconds float64  `json:"durationSeconds"`
	AnalyzedSeconds float64  `json:"analyzedSeconds"`
	EF1             *float64 `json:"ef1,omitempty"`
	EF2             *float64 `json:"ef2,omitempty"`
	Decoupling      *float64 `json:"decoupling,omitempty"`
	PaceDecay       *float64 `json:"paceDecay,omitempty"`
}

type EfficiencyProduct struct {
	WorkoutID          string   `json:"workoutID"`
	EfficiencyMPerBeat *float64 `json:"efficiencyMPerBeat,omitempty"`
	SpeedMPerMin       *float64 `json:"speedMPerMin,omitempty"`
	HeartRateBPM       *float64 `json:"heartRateBPM,omitempty"`
	ValidSeconds       float64  `json:"validSeconds"`
}

type CriticalSpeedProduct struct {
	CSMPS        *float64 `json:"csMPS,omitempty"`
	DPrimeMeters *float64 `json:"dPrimeMeters,omitempty"`
	R2           *float64 `json:"r2,omitempty"`
	WorkoutCount int      `json:"workoutCount"`
}

type SettlingProduct struct {
	Hours    *float64 `json:"hours,omitempty"`
	Bucket   string   `json:"bucket,omitempty"`
	Load     float64  `json:"load"`
	Baseline float64  `json:"baseline"`
	Score    float64  `json:"score"`
}

type ProductSnapshot struct {
	SchemaVersion string            `json:"schemaVersion"`
	AsOf          int64             `json:"asOfUs"`
	CalendarDate  string            `json:"calendarDate"`
	Assignment    ProductAssignment `json:"assignment"`
	Experimental  bool              `json:"experimental"`
	Items         []ProductItem     `json:"items"`
}

// ProductStatusOf maps the diagnostics lifecycle to the product vocabulary.
func ProductStatusOf(s Status) ProductStatus {
	switch s {
	case StatusOK:
		return ProductAvailable
	case StatusPartial:
		return ProductPartial
	case StatusCollecting:
		return ProductCollecting
	case StatusUnstable:
		return ProductUnstable
	default:
		return ProductInsufficient
	}
}

// ProductStatusOfPerformance maps the running-ability lifecycle to the same
// product vocabulary.
func ProductStatusOfPerformance(s string) ProductStatus {
	switch s {
	case "available", "complete":
		return ProductAvailable
	case "collecting", "collecting_baseline":
		return ProductCollecting
	case "stale", "expired":
		return ProductStale
	case "partial", "limited":
		return ProductPartial
	default:
		return ProductInsufficient
	}
}
