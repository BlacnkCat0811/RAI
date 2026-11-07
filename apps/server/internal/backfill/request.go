package backfill

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// DayLayout is the only accepted civil-day representation.
	DayLayout = "2006-01-02"
	// MaxOutputDays bounds one explicit backfill request.
	MaxOutputDays = 366
	// MaxBlockDays bounds one execution block.
	MaxBlockDays = 31
)

// View selects which historical contract a request addresses.
type View string

const (
	// ViewRevised recomputes with the facts currently allowed for the dataset.
	ViewRevised View = "revised"
	// ViewAsKnown reads immutable historical results and their lineage. It may
	// not be reconstructible from current raw facts; that is reported by the
	// execution layer as historical_input_unavailable rather than fabricated.
	ViewAsKnown View = "as_known"
)

// Operation is the administrative intent recorded with the plan identity.
type Operation string

const (
	OperationBackfill      Operation = "backfill"
	OperationManualRebuild Operation = "manual_rebuild"
	OperationDryRun        Operation = "dry_run"
)

// Request is the explicit, complete identity of one bounded backfill. There is
// no implicit "all datasets" or "all history" request.
type Request struct {
	DatasetID   string    `json:"datasetID"`
	ManifestSHA string    `json:"manifestSHA"`
	StackID     string    `json:"stackID"`
	From        string    `json:"from"`
	Through     string    `json:"through"`
	AsOf        string    `json:"asOf"`
	View        View      `json:"view"`
	Operation   Operation `json:"operation"`
	Actor       string    `json:"actor"`
	BlockDays   int       `json:"blockDays"`
}

var (
	ErrInvalidRequest = errors.New("backfill_invalid_request")
	ErrFuture         = errors.New("backfill_future")
	ErrCapacity       = errors.New("backfill_capacity")
	ErrMissingInputs  = errors.New("backfill_missing_inputs")
)

// Validate checks the request without consulting a clock or storage. The
// caller supplies AsOf and (through Facts) any notion of "today".
func (r Request) Validate() error {
	if strings.TrimSpace(r.DatasetID) == "" {
		return fmt.Errorf("%w: datasetID is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(r.StackID) == "" {
		return fmt.Errorf("%w: stackID is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(r.Actor) == "" {
		return fmt.Errorf("%w: actor is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(r.ManifestSHA) == "" {
		return fmt.Errorf("%w: manifestSHA is required", ErrInvalidRequest)
	}
	from, err := parseDay(r.From)
	if err != nil {
		return fmt.Errorf("%w: from must be YYYY-MM-DD", ErrInvalidRequest)
	}
	through, err := parseDay(r.Through)
	if err != nil {
		return fmt.Errorf("%w: through must be YYYY-MM-DD", ErrInvalidRequest)
	}
	if _, err := parseDay(r.AsOf); err != nil {
		return fmt.Errorf("%w: asOf must be YYYY-MM-DD", ErrInvalidRequest)
	}
	if through.Before(from) {
		return fmt.Errorf("%w: through must not precede from", ErrInvalidRequest)
	}
	days := int(through.Sub(from)/(24*time.Hour)) + 1
	if days < 1 || days > MaxOutputDays {
		return fmt.Errorf("%w: output days must be 1..%d, got %d", ErrInvalidRequest, MaxOutputDays, days)
	}
	if r.BlockDays < 1 || r.BlockDays > MaxBlockDays {
		return fmt.Errorf("%w: blockDays must be 1..%d", ErrInvalidRequest, MaxBlockDays)
	}
	switch r.View {
	case ViewRevised, ViewAsKnown:
	default:
		return fmt.Errorf("%w: unsupported view %q", ErrInvalidRequest, r.View)
	}
	switch r.Operation {
	case OperationBackfill, OperationManualRebuild, OperationDryRun:
	default:
		return fmt.Errorf("%w: unsupported operation %q", ErrInvalidRequest, r.Operation)
	}
	return nil
}

// BlockStatus is the pure lifecycle state of one block. Only the storage/job
// layer may transition it; Plan always produces pending blocks.
type BlockStatus string

const (
	BlockPending    BlockStatus = "pending"
	BlockRunning    BlockStatus = "running"
	BlockComplete   BlockStatus = "complete"
	BlockFailed     BlockStatus = "failed"
	BlockCancelled  BlockStatus = "cancelled"
	BlockSuperseded BlockStatus = "superseded"
)

// Block is one deterministic unit of backfill work.
type Block struct {
	Index      int         `json:"index"`
	From       string      `json:"from"`
	Through    string      `json:"through"`
	InputHash  string      `json:"inputHash"`
	TargetAsOf string      `json:"targetAsOf"`
	Status     BlockStatus `json:"status"`
}

// DAG warmup metadata. These are lookback days owned by each model before the
// first requested output day; they are deliberately not one global window.
// Readiness consumes the prior day's load state, so its own one-day edge is
// combined with the load model's transitive window by the execution layer.
const (
	LoadWarmupDays      = 180
	AbilityWarmupDays   = 102
	VO2WarmupDays       = 47
	RHRWarmupDays       = 28
	ReadinessWarmupDays = 1
)

// WarmupSpec is one static DAG edge.
type WarmupSpec struct {
	Model string `json:"model"`
	Days  int    `json:"days"`
}

// WarmupSpecs returns the fixed DAG warmup metadata in a stable order.
func WarmupSpecs() []WarmupSpec {
	return []WarmupSpec{
		{Model: ModelLoad, Days: LoadWarmupDays},
		{Model: ModelAbility, Days: AbilityWarmupDays},
		{Model: ModelVO2, Days: VO2WarmupDays},
		{Model: ModelRHR, Days: RHRWarmupDays},
		{Model: ModelReadiness, Days: ReadinessWarmupDays},
	}
}

// Warmup is the concrete input window a plan needs before From.
type Warmup struct {
	Model   string `json:"model"`
	Days    int    `json:"days"`
	From    string `json:"from"`
	Through string `json:"through"`
}

// Facts are planning facts already established by the caller. Today is an
// explicit civil day, never time.Now; an empty Today disables the future check.
// MissingInputs lists unavailable manifests/artifacts/parameter snapshots.
// MaxBlocks == 0 means no explicit block-count capacity; a negative value is
// rejected by Plan.
type Facts struct {
	Today         string   `json:"today,omitempty"`
	MissingInputs []string `json:"missingInputs,omitempty"`
	MaxBlocks     int      `json:"maxBlocks,omitempty"`
}

// PlanResult is the deterministic output of the planning layer.
type PlanResult struct {
	Request    Request  `json:"request"`
	OutputDays int      `json:"outputDays"`
	Blocks     []Block  `json:"blocks"`
	Warmup     []Warmup `json:"warmup"`
}

// Plan validates the request against caller-supplied facts and splits it into
// blocks of at most BlockDays (and never more than MaxBlockDays). It does not
// read storage or the clock and it never inspects health values.
func Plan(req Request, facts Facts) (PlanResult, error) {
	if err := req.Validate(); err != nil {
		return PlanResult{}, err
	}
	if facts.MaxBlocks < 0 {
		return PlanResult{}, fmt.Errorf("%w: maxBlocks must not be negative", ErrInvalidRequest)
	}
	if facts.Today != "" {
		today, err := parseDay(facts.Today)
		if err != nil {
			return PlanResult{}, fmt.Errorf("%w: facts.today must be YYYY-MM-DD", ErrInvalidRequest)
		}
		through, _ := parseDay(req.Through)
		asOf, _ := parseDay(req.AsOf)
		if through.After(today) {
			return PlanResult{}, fmt.Errorf("%w: through %s is after planning day %s", ErrFuture, req.Through, facts.Today)
		}
		if asOf.After(today) {
			return PlanResult{}, fmt.Errorf("%w: asOf %s is after planning day %s", ErrFuture, req.AsOf, facts.Today)
		}
	}
	if len(facts.MissingInputs) > 0 {
		return PlanResult{}, fmt.Errorf("%w: %s", ErrMissingInputs, strings.Join(facts.MissingInputs, ","))
	}

	from, _ := parseDay(req.From)
	through, _ := parseDay(req.Through)
	blocks := make([]Block, 0, 1+(int(through.Sub(from)/(24*time.Hour))/req.BlockDays))
	for cursor, index := from, 0; !cursor.After(through); cursor, index = cursor.AddDate(0, 0, req.BlockDays), index+1 {
		blockThrough := cursor.AddDate(0, 0, req.BlockDays-1)
		if blockThrough.After(through) {
			blockThrough = through
		}
		blockFrom := cursor.Format(DayLayout)
		blockThroughDay := blockThrough.Format(DayLayout)
		blocks = append(blocks, Block{
			Index:      index,
			From:       blockFrom,
			Through:    blockThroughDay,
			InputHash:  blockInputHash(req, index, blockFrom, blockThroughDay),
			TargetAsOf: req.AsOf,
			Status:     BlockPending,
		})
	}
	if len(blocks) == 0 {
		return PlanResult{}, fmt.Errorf("%w: plan produced no blocks", ErrInvalidRequest)
	}
	if facts.MaxBlocks > 0 && len(blocks) > facts.MaxBlocks {
		return PlanResult{}, fmt.Errorf("%w: %d blocks exceed capacity %d", ErrCapacity, len(blocks), facts.MaxBlocks)
	}

	warmup := make([]Warmup, 0, len(WarmupSpecs()))
	for _, spec := range WarmupSpecs() {
		warmup = append(warmup, Warmup{
			Model:   spec.Model,
			Days:    spec.Days,
			From:    addDays(req.From, -spec.Days),
			Through: addDays(req.From, -1),
		})
	}

	return PlanResult{
		Request:    req,
		OutputDays: int(through.Sub(from)/(24*time.Hour)) + 1,
		Blocks:     blocks,
		Warmup:     warmup,
	}, nil
}

// blockInputHash is a stable SHA-256 over the immutable block identity. It
// includes dataset/manifest/stack, the request and block date spans, asOf and
// view as required, plus operation, block size and index so that a parameter or
// chunking change cannot reuse an old completion. Actor is audit identity, not
// a computation input.
func blockInputHash(req Request, index int, from, through string) string {
	payload := struct {
		DatasetID      string    `json:"datasetID"`
		ManifestSHA    string    `json:"manifestSHA"`
		StackID        string    `json:"stackID"`
		RequestFrom    string    `json:"requestFrom"`
		RequestThrough string    `json:"requestThrough"`
		BlockFrom      string    `json:"blockFrom"`
		BlockThrough   string    `json:"blockThrough"`
		AsOf           string    `json:"asOf"`
		View           View      `json:"view"`
		Operation      Operation `json:"operation"`
		BlockDays      int       `json:"blockDays"`
		Index          int       `json:"index"`
	}{
		DatasetID:      req.DatasetID,
		ManifestSHA:    req.ManifestSHA,
		StackID:        req.StackID,
		RequestFrom:    req.From,
		RequestThrough: req.Through,
		BlockFrom:      from,
		BlockThrough:   through,
		AsOf:           req.AsOf,
		View:           req.View,
		Operation:      req.Operation,
		BlockDays:      req.BlockDays,
		Index:          index,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		// A struct of strings and integers cannot fail to marshal.
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func parseDay(value string) (time.Time, error) {
	if len(value) != len(DayLayout) {
		return time.Time{}, fmt.Errorf("invalid day")
	}
	parsed, err := time.Parse(DayLayout, value)
	if err != nil || parsed.Format(DayLayout) != value {
		return time.Time{}, fmt.Errorf("invalid day")
	}
	return parsed, nil
}

func addDays(value string, days int) string {
	parsed, err := time.Parse(DayLayout, value)
	if err != nil {
		return ""
	}
	return parsed.AddDate(0, 0, days).Format(DayLayout)
}
