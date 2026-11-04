// Package vo2maxexternal selects an existing external fact. It never estimates
// VO2max. Every time and every piece of control evidence is supplied by callers.
package vo2maxexternal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
)

const (
	Model                  = "rai.vo2max.external_projection/v1"
	Algorithm              = "rai.vo2max.external_projection"
	JobKind                = "analysis.vo2max_external_projection"
	Scope                  = "external/vo2max/v1/athlete/current"
	FactsScope             = "external/vo2max/facts/v1"
	SelectionScope         = "external/vo2max/selection/v1"
	ArtifactKey            = "vo2max_external_projection"
	Schema                 = "rai.vo2max.external_projection/1"
	InputSchema            = "rai.vo2max.external_input/1"
	SelectionSchema        = "rai.vo2max.source_selection/1"
	Family                 = "vo2max_external_candidate"
	Metric                 = "vo2max_external_value"
	Unit                   = "mL/kg/min"
	Apple                  = "apple_watch_estimate"
	DayUs            int64 = 86400000000
	MaxRefs                = 1024
	MaxEpochs              = 32
	MaxTies                = 256
	MaxArtifactBytes       = 131072
)

var ErrLimit = errors.New("analysis_input_limit")
var ErrInvalid = errors.New("analysis_invalid_input")
var ErrHistorical = errors.New("historical_snapshot_unavailable")

type Config struct {
	CanonicalVersion   string `json:"canonicalVersion"`
	InputSchema        string `json:"inputSchema"`
	OriginFilter       string `json:"originFilter"`
	MaxRefs            int    `json:"maxRefs"`
	MaxEpochs          int    `json:"maxEpochs"`
	MaxTies            int    `json:"maxLatestTies"`
	ReasonCodesVersion string `json:"reasonCodesVersion"`
	Schema             string `json:"schemaVersion"`
	FreshUs            int64  `json:"freshUs"`
	ExpireUs           int64  `json:"expireUs"`
	OriginRule         string `json:"originRule"`
	SelectionRule      string `json:"selectionRule"`
	ConflictRule       string `json:"conflictRule"`
}

func DefaultConfig() Config {
	return Config{Schema: "rai.vo2max.external_config/1", FreshUs: 30 * DayUs, ExpireUs: 90 * DayUs, OriginRule: "rai.healthkit.vo2max.origin/1", SelectionRule: "explicit_epoch/1", ConflictRule: "exact_end_support/1", CanonicalVersion: "v1", InputSchema: InputSchema, OriginFilter: Apple, MaxRefs: MaxRefs, MaxEpochs: MaxEpochs, MaxTies: MaxTies, ReasonCodesVersion: "rai.vo2max.external_reasons/1"}
}
func (c Config) Hash() string { return Hash(c) }

// Only the frozen v1 policy is executable; a future policy needs a reviewed version.
func (c Config) Valid() bool { return c == DefaultConfig() }
func Hash(v any) string {
	b, e := json.Marshal(v)
	if e != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type Fact struct {
	ID            string  `json:"id"`
	Hash          string  `json:"hash"`
	Epoch         string  `json:"epoch"`
	Start         int64   `json:"startUs"`
	End           int64   `json:"endUs"`
	KnownRevision int64   `json:"knownRevision"`
	Value         float64 `json:"value"`
	Origin        string  `json:"originClass"`
	OriginReason  string  `json:"originReason"`
	SupportHash   string  `json:"supportHash"`
	SemanticHash  string  `json:"semanticHash"`
	SyncKey       string  `json:"syncKey"` // hash of source-scoped identifier, never raw text
	TestType      *int64  `json:"testType"`
	Calibrated    *bool   `json:"calibrated"`
	Supported     bool    `json:"supported"`
}
type Tombstone struct {
	ID       string `json:"id"`
	Hash     string `json:"hash"`
	Epoch    string `json:"epoch"`
	End      *int64 `json:"endUs"`
	Revision int64  `json:"revision"`
	Origin   string `json:"originClass"`
}
type Selection struct {
	ID               string `json:"id"`
	Hash             string `json:"hash"`
	Epoch            string `json:"sourceEpoch"`
	EffectiveFrom    int64  `json:"effectiveFromUs"`
	EffectiveThrough *int64 `json:"effectiveThroughUs"`
	RecordedAt       int64  `json:"recordedAtUs"`
	Supersedes       string `json:"supersedes"`
	Revoked          bool   `json:"revoked"`
}
type Coverage struct {
	Stream           string `json:"streamID"`
	Fingerprint      string `json:"queryFingerprint"`
	Sequence         int64  `json:"sequence"`
	BootstrapStart   *int64 `json:"bootstrapStartUs"`
	QueryCompletedAt *int64 `json:"queryCompletedAtUs"`
	Complete         bool   `json:"complete"`
	Hash             string `json:"hash"`
}
type ExternalVO2Snapshot struct {
	Dataset              string      `json:"datasetID"`
	DatasetEpoch         string      `json:"datasetGeneration"`
	Collector            string      `json:"collectorEpoch"`
	Generation           int64       `json:"inputGeneration"`
	AsOf                 int64       `json:"asOf"`
	CapturedAt           int64       `json:"capturedAt"`
	KnownThroughRevision int64       `json:"knownThroughRevision"`
	Config               Config      `json:"config"`
	Assignment           string      `json:"assignmentID"`
	View                 string      `json:"view"`
	HistoricalCapable    bool        `json:"historicalCapable"`
	Selections           []Selection `json:"sourceSelection"`
	Facts                []Fact      `json:"facts"`
	Tombstones           []Tombstone `json:"tombstones"`
	Coverage             Coverage    `json:"coverage"`
}

func Canonical(in ExternalVO2Snapshot) ExternalVO2Snapshot {
	in.Facts = append([]Fact{}, in.Facts...)
	in.Tombstones = append([]Tombstone{}, in.Tombstones...)
	in.Selections = append([]Selection{}, in.Selections...)
	sort.Slice(in.Facts, func(i, j int) bool { return in.Facts[i].ID < in.Facts[j].ID })
	sort.Slice(in.Tombstones, func(i, j int) bool { return in.Tombstones[i].ID < in.Tombstones[j].ID })
	sort.Slice(in.Selections, func(i, j int) bool { return in.Selections[i].ID < in.Selections[j].ID })
	return in
}

type Selected struct {
	End             int64  `json:"endUs"`
	Start           int64  `json:"startUs"`
	TestType        *int64 `json:"testType"`
	Calibrated      *bool  `json:"calibrated"`
	EquivalentCount int    `json:"equivalentEvidenceCount"`
}
type Result struct {
	ModelIdentity         string         `json:"modelIdentity"`
	DatasetCommitRevision int64          `json:"datasetCommitRevision"`
	SourceLineage         Lineage        `json:"sourceLineage"`
	Evidence              Evidence       `json:"evidence"`
	Schema                string         `json:"schemaVersion"`
	Dataset               string         `json:"datasetID"`
	DatasetEpoch          string         `json:"datasetGeneration"`
	Collector             string         `json:"collectorEpoch"`
	Value                 *float64       `json:"value"`
	Unit                  string         `json:"unit"`
	Method                string         `json:"method"`
	Provider              string         `json:"provider"`
	Origin                *string        `json:"originClass"`
	Status                string         `json:"status"`
	Confidence            *float64       `json:"confidence"`
	Quality               string         `json:"quality"`
	ParameterVersion      *string        `json:"parameterVersion"`
	FeatureVersion        *string        `json:"featureVersion"`
	ModelVersion          string         `json:"modelVersion"`
	InputSchema           string         `json:"inputSchemaVersion"`
	ConfigHash            string         `json:"configHash"`
	Generation            int64          `json:"inputGeneration"`
	AsOf                  int64          `json:"asOf"`
	CalculatedAt          int64          `json:"calculatedAt"`
	EvaluationAt          int64          `json:"evaluationAt"`
	KnownThroughRevision  int64          `json:"knownThroughRevision"`
	View                  string         `json:"view"`
	SourceEpoch           *string        `json:"sourceEpoch"`
	SelectionID           *string        `json:"sourceSelectionInputID"`
	Assignment            string         `json:"assignmentID"`
	Coverage              Coverage       `json:"coverage"`
	Selected              *Selected      `json:"selectedSample"`
	FactRefs              [][2]string    `json:"factRefs"`
	TombstoneRefs         [][2]string    `json:"tombstoneRefs"`
	SelectionRefs         [][2]string    `json:"selectionRefs"`
	Reasons               []string       `json:"reasonCodes"`
	Excluded              map[string]int `json:"excluded"`
}

type Lineage struct {
	OriginRule       string  `json:"originRule"`
	SourceEpoch      *string `json:"sourceEpoch"`
	SelectionInputID *string `json:"selectionInputID"`
}
type Evidence struct {
	FactCount      int    `json:"factCount"`
	TombstoneCount int    `json:"tombstoneCount"`
	SelectionCount int    `json:"selectionCount"`
	Laboratory     string `json:"laboratory"`
}
