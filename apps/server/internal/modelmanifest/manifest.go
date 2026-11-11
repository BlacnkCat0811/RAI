// Package modelmanifest defines the immutable, canonical component manifest
// shared by scopes, assignments, runs and the product reader. It is pure: the
// package performs no I/O and does not consult a clock.
package modelmanifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

const (
	Schema           = "rai.model_manifest/1"
	ProductSchema    = "rai.athlete_model/2"
	MappingVersion   = "rai.product_metric_mapping/2"
	CalendarPolicy   = "civil-day-iana-dst-gap-overlap/v1"
	AnnotationPolicy = "versioned-effort-annotation-known-at/v1"
	InputRegistry    = "rai.model_input_registry/1"
)

var ErrInvalid = errors.New("model_manifest_invalid")

type Component struct {
	Name           string `json:"name"`
	ModelID        string `json:"modelId"`
	Version        string `json:"version"`
	Schema         string `json:"schema"`
	InputSchema    string `json:"inputSchema,omitempty"`
	JobKind        string `json:"jobKind,omitempty"`
	ArtifactKey    string `json:"artifactKey,omitempty"`
	ConfigHash     string `json:"configHash"`
	SourcePolicy   string `json:"sourcePolicy"`
	AnnotationMode string `json:"annotationMode,omitempty"`
	ConsumerSchema string `json:"consumerSchema,omitempty"`
}

type Manifest struct {
	Schema                   string      `json:"schema"`
	Features                 Component   `json:"features"`
	Load                     Component   `json:"load"`
	LoadState                Component   `json:"loadState"`
	RunningAbility           Component   `json:"runningAbility"`
	VO2Max                   Component   `json:"vo2max"`
	Recovery                 Component   `json:"recovery"`
	Readiness                Component   `json:"readiness"`
	Diagnostics              []Component `json:"diagnostics"`
	ExternalVO2              Component   `json:"externalVO2"`
	ProductSchema            string      `json:"productSchema"`
	MappingVersion           string      `json:"mappingVersion"`
	InputRegistry            string      `json:"inputRegistry"`
	CalendarPolicy           string      `json:"calendarPolicy"`
	AnnotationPolicy         string      `json:"annotationPolicy"`
	ConsumerVersions         []string    `json:"consumerVersions"`
	EvidenceMode             string      `json:"evidenceMode"`
	PhysiologicallyValidated bool        `json:"physiologicallyValidated"`
}

func digest(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// Current describes the managed synthetic release and its component contracts.
func Current() Manifest {
	return Manifest{
		Schema:         Schema,
		Features:       Component{Name: "workout_features", ModelID: "rai.workout_features", Version: "v1", Schema: "rai.workout_features/1", InputSchema: "rai.workout_features.input/1", JobKind: StackJobKind, ArtifactKey: "workout_features", ConfigHash: digest("features:normalized-workout/v1"), SourcePolicy: "recorded-running;source-policy/v1", ConsumerSchema: "rai.workout_features/1"},
		Load:           Component{Name: "training_load", ModelID: "rai.training_load.hrr_integral", Version: "v2", Schema: "rai.training_load/2", InputSchema: "rai.training_load.input/2", ConfigHash: digest("load:tiers.98-30s/.80-180s;hrr-clamp-0-1;summary.35;ratio1;horizon180;reference-v2;overlap-knownness;workout-isolated;hrref-latest-effective-no-expiry;pace-approximation/v2.3"), SourcePolicy: "recorded-running;reference-v2;stable-source-key/v2.1", ConsumerSchema: "rai.training_load/2"},
		LoadState:      Component{Name: "load_state", ModelID: "rai.load_state.exp_finite", Version: "v2", Schema: "rai.load_state/2", InputSchema: "rai.load_state.input/2", JobKind: StackJobKind, ArtifactKey: "load_state", ConfigHash: digest("load-state:tau7;tau42;horizon180;finite-tail-certified;unknown-mass.15/.10;ring-mean-imputation/v2.1"), SourcePolicy: "recorded-running;reference-v2;stable-source-key/v2.1", ConsumerSchema: "rai.load_state/2"},
		RunningAbility: Component{Name: "running_ability", ModelID: "rai.running_ability", Version: "v2", Schema: "rai.performance/2", InputSchema: "rai.running_ability.input/2", JobKind: StackJobKind, ArtifactKey: "performance_state_v2", ConfigHash: digest("ability:window90;quantile.8;gamma.7-1;step.001;recency45;residual.06;outlier.10;confirmation.05;loo.05;cap.55;maximal.50;cap.7;stable-source-key;grade-adjusted-minetti.08;envelope-top3-median/v2.2"), SourcePolicy: "recorded-running;stable-source-key;grade-adjusted/v2.2", AnnotationMode: AnnotationPolicy, ConsumerSchema: "rai.running_ability/2"},
		VO2Max:         Component{Name: "vo2max_submax", ModelID: "rai.vo2max.submax", Version: "v1", Schema: "rai.vo2max.submax/1", InputSchema: "rai.vo2max.submax.input/1", JobKind: StackJobKind, ArtifactKey: "vo2max_submax", ConfigHash: digest("vo2:3.5+0.2v/r;25min;10min-warmup;5min;42d;q.exp28;waterfill.333;mad.04;winsor2;outlier.20;confirm.14d.10;7d;8corners;loo;experimental;hrmax-reference-identity;minetti-grade.05;env-weighted-0-30c"), SourcePolicy: "recorded-running;hr-reference-v2;stable-source-key/v1.1", AnnotationMode: AnnotationPolicy, ConsumerSchema: "rai.vo2max.submax/1"},
		Recovery:       Component{Name: "physiological_recovery", ModelID: "rai.recovery.physiological", Version: "v2", Schema: "rai.recovery.physiological/2", InputSchema: "rai.recovery.input/2", JobKind: StackJobKind, ArtifactKey: "physiological_recovery", ConfigHash: digest("recovery:night00-06;ln-median;28d;14/7-3;circular-medoid;typed-target;persistent3;autonomic+sleep;stable-source-key;source-priority;asleep-sdnn-20-12;deviation-levels/v2.2"), SourcePolicy: "healthkit-readonly;night-context;stable-source-key/v2.1", ConsumerSchema: "rai.recovery.physiological/2"},
		Readiness:      Component{Name: "training_readiness", ModelID: "rai.readiness.training", Version: "v3", Schema: "rai.readiness.training/3", InputSchema: "rai.readiness.input/3", JobKind: StackJobKind, ArtifactKey: "training_readiness", ConfigHash: digest("readiness:180d;42dB;5d;14d;floor1;allowlist-v2;known-any-tier;unknown-burden.15-typical-days;habitual-burden-28d-floor.5-min.35/v3.2"), SourcePolicy: "recorded-running;allowlist-load-v2-recovery-v2;decayed-unknown/v3.1", ConsumerSchema: "rai.readiness.training/3"},
		Diagnostics: []Component{
			{Name: "workout_durability", ModelID: "rai.workout_durability", Version: "v1", Schema: "rai.workout_durability/1", ConfigHash: digest("durability:60min;10min;half20;cv.10;support.90;trend42/v1"), SourcePolicy: "recorded-running;source-epoch/v1", ConsumerSchema: "rai.workout_durability/1"},
			{Name: "aerobic_efficiency", ModelID: "rai.aerobic_efficiency", Version: "v1", Schema: "rai.aerobic_efficiency/1", ConfigHash: digest("aerobic:v/min/hr;300s;support.90;context/v1"), SourcePolicy: "recorded-running;source-epoch/v1", ConsumerSchema: "rai.aerobic_efficiency/1"},
			{Name: "critical_speed", ModelID: "rai.critical_speed", Version: "v1", Schema: "rai.critical_speed/1", ConfigHash: digest("cs:42d;3-15min;3workouts;3durations;2.5;wls;loo.05/rmse.03/v1"), SourcePolicy: "recorded-running;source-epoch/v1", ConsumerSchema: "rai.critical_speed/1"},
			{Name: "load_settling", ModelID: "rai.load_settling", Version: "v1", Schema: "rai.load_settling/1", ConfigHash: digest("settling:36h;ln(D/.25B);P85;buckets12/24/36/48/v1"), SourcePolicy: "recorded-running;load-v2/v1", ConsumerSchema: "rai.load_settling/1"},
		},
		ExternalVO2:      Component{Name: "external_vo2max", ModelID: "rai.vo2max.external_projection", Version: "v1", Schema: "rai.vo2max.external_projection/1", InputSchema: "rai.vo2max.external_input/1", JobKind: StackJobKind, ArtifactKey: "vo2max_external_projection", ConfigHash: digest("external:apple-healthkit;no-calibration;stable-source-selection/v1.1"), SourcePolicy: "healthkit-readonly;external/v1", ConsumerSchema: "rai.vo2max.external_projection/1"},
		ProductSchema:    ProductSchema,
		MappingVersion:   MappingVersion,
		InputRegistry:    InputRegistry,
		CalendarPolicy:   CalendarPolicy,
		AnnotationPolicy: AnnotationPolicy,
		ConsumerVersions: []string{"ios/1", "training-adapter/2", "http/v2"},
		EvidenceMode:     "candidate_then_active",
	}
}

func (m Manifest) Validate() error {
	all := []Component{m.Features, m.Load, m.LoadState, m.RunningAbility, m.VO2Max, m.Recovery, m.Readiness, m.ExternalVO2}
	all = append(all, m.Diagnostics...)
	seen := map[string]bool{}
	for _, c := range all {
		if c.Name == "" || c.ModelID == "" || c.Version == "" || c.Schema == "" || len(c.ConfigHash) != 64 || c.SourcePolicy == "" {
			return ErrInvalid
		}
		if seen[c.ModelID+"/"+c.Version] {
			return ErrInvalid
		}
		seen[c.ModelID+"/"+c.Version] = true
	}
	if m.Schema != Schema || m.ProductSchema != ProductSchema || m.MappingVersion != MappingVersion || len(m.Diagnostics) != 4 || m.PhysiologicallyValidated {
		return ErrInvalid
	}
	return nil
}

// Hash returns the canonical SHA-256 of the complete manifest. Maps are not
// used, so encoding/json ordering is the struct field order and is stable.
func Hash(m Manifest) (string, error) {
	if e := m.Validate(); e != nil {
		return "", e
	}
	b, e := json.Marshal(m)
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func CurrentHash() (string, error) { return Hash(Current()) }

// StackID isolates every release and configuration from prior assignments.
func StackID() string {
	h, err := CurrentHash()
	if err != nil {
		panic(err)
	}
	return "rai-v2-" + h[:16]
}

const StackJobKind = "analysis.model_stack_v2"

func DayScope(stack, date string) string {
	return "model/" + stack + "/athlete/day/v2/" + date
}
