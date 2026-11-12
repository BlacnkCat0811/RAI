// Package modelmanifest defines the immutable, canonical component manifest
// shared by scopes, assignments, runs and the product reader. It is pure: the
// package performs no I/O and does not consult a clock.
package modelmanifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	rf "rai/server/internal/runfitness"
	wf "rai/server/internal/workoutfeatures"
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
	SessionRecovery          *Component  `json:"sessionRecovery,omitempty"`
	AbilityEstimate          *Component  `json:"abilityEstimate,omitempty"`
	LoadFocus                *Component  `json:"loadFocus,omitempty"`
}

// ProductSchema4 is the running-fitness product contract.
const ProductSchema4 = "rai.athlete_model/4"
const MappingVersion4 = "rai.product_metric_mapping/4"

func digest(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// Current describes the managed synthetic release and its component contracts.
func baselineManifest() Manifest {
	return Manifest{
		Schema:         Schema,
		Features:       Component{Name: "workout_features", ModelID: "rai.workout_features", Version: "v1", Schema: "rai.workout_features/1", InputSchema: "rai.workout_features.input/1", JobKind: StackJobKind, ArtifactKey: "workout_features", ConfigHash: digest("features:normalized-workout/v1"), SourcePolicy: "recorded-running;source-policy/v1", ConsumerSchema: "rai.workout_features/1"},
		Load:           Component{Name: "training_load", ModelID: "rai.training_load.hrr_integral", Version: "v2", Schema: "rai.training_load/2", InputSchema: "rai.training_load.input/2", ConfigHash: digest("load:tiers.98-30s/.80-180s;hrr-clamp-0-1;summary.35;ratio1;horizon180;reference-v2;overlap-knownness;workout-isolated;hrref-latest-effective-no-expiry;pace-approximation/v2.3"), SourcePolicy: "recorded-running;reference-v2;stable-source-key/v2.1", ConsumerSchema: "rai.training_load/2"},
		LoadState:      Component{Name: "load_state", ModelID: "rai.load_state.exp_finite", Version: "v2", Schema: "rai.load_state/2", InputSchema: "rai.load_state.input/2", JobKind: StackJobKind, ArtifactKey: "load_state", ConfigHash: digest("load-state:tau7;tau42;horizon180;finite-tail-certified;unknown-mass.15/.10;ring-mean-imputation/v2.1"), SourcePolicy: "recorded-running;reference-v2;stable-source-key/v2.1", ConsumerSchema: "rai.load_state/2"},
		RunningAbility: Component{Name: "running_ability", ModelID: "rai.running_ability", Version: "v2", Schema: "rai.performance/2", InputSchema: "rai.running_ability.input/2", JobKind: StackJobKind, ArtifactKey: "performance_state_v2", ConfigHash: digest("ability:window90;quantile.8;gamma.7-1;step.001;recency45;residual.06;outlier.10;confirmation.05;loo.05;cap.55;maximal.50;cap.7;stable-source-key;grade-adjusted-minetti.08;envelope-top3-median/v2.2"), SourcePolicy: "recorded-running;stable-source-key;grade-adjusted/v2.2", AnnotationMode: AnnotationPolicy, ConsumerSchema: "rai.running_ability/2"},
		VO2Max:         Component{Name: "vo2max_submax", ModelID: "rai.vo2max.submax", Version: "v1", Schema: "rai.vo2max.submax/1", InputSchema: "rai.vo2max.submax.input/1", JobKind: StackJobKind, ArtifactKey: "vo2max_submax", ConfigHash: digest("vo2:3.5+0.2v/r;25min;10min-warmup;5min;42d;q.exp28;waterfill.333;mad.04;winsor2;outlier.20;confirm.14d.10;7d;8corners;loo;experimental;hrmax-reference-identity;minetti-grade.05;env-weighted-0-30c"), SourcePolicy: "recorded-running;hr-reference-v2;stable-source-key/v1.1", AnnotationMode: AnnotationPolicy, ConsumerSchema: "rai.vo2max.submax/1"},
		Recovery:       Component{Name: "physiological_recovery", ModelID: "rai.recovery.physiological", Version: "v2", Schema: "rai.recovery.physiological/2", InputSchema: "rai.recovery.input/2", JobKind: StackJobKind, ArtifactKey: "physiological_recovery", ConfigHash: digest("recovery:night00-06;ln-median;28d;14/7-3;circular-medoid;typed-target;persistent3;autonomic+sleep;stable-source-key;source-priority;asleep-sdnn-20-12;deviation-levels;evaluation-day-tombstones/v2.3"), SourcePolicy: "healthkit-readonly;night-context;stable-source-key/v2.1", ConsumerSchema: "rai.recovery.physiological/2"},
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

// schema3Manifest is the deployed schema3 release (frozen in modelrelease).
func schema3Manifest() Manifest {
	m := baselineManifest()
	m.ProductSchema = "rai.athlete_model/3"
	m.MappingVersion = "rai.product_metric_mapping/3"
	m.ConsumerVersions = []string{"ios/3", "training-adapter/3", "http/v2"}
	m.Features.ConfigHash = digest("features:normalized-workout/v1;structure-ten-second-native5s-grade03/v1")
	m.Recovery.Version = "v3"
	m.Recovery.Schema = "rai.recovery.physiological/3"
	m.Recovery.InputSchema = "rai.recovery.input/3"
	m.Recovery.ConsumerSchema = m.Recovery.Schema
	m.Recovery.ConfigHash = digest("night-block30m;trend7-4;decay3d;weight-cap.35;baseline28-7/14/28;continuous82;single-missing48h;no-deleted-fallback/v3")
	m.Readiness.Version = "v4"
	m.Readiness.Schema = "rai.readiness.training/4"
	m.Readiness.InputSchema = "rai.readiness.input/4"
	m.Readiness.ConsumerSchema = m.Readiness.Schema
	m.Readiness.ConfigHash = digest("min-phys-session-weekly;continuous82;unknown.15-days;range<=10;estimated<=75;session-fatigue/v4")
	m.SessionRecovery = &Component{Name: "session_recovery", ModelID: "rai.session_recovery", Version: "v1", Schema: "rai.session_recovery/1", ArtifactKey: "session_recovery", JobKind: StackJobKind, ConfigHash: digest("AU/B^.6;u<=4;tau18-72;short-repeat-proxy;threshold.25;binary.25h;max168h;tail180d-4096;material.02;unknown.5-2B-tau48;baseline28-fallback90/v1"), SourcePolicy: "recorded-running;pre-workout-baseline", ConsumerSchema: "rai.session_recovery/1"}
	m.AbilityEstimate = &Component{Name: "running_ability_estimate", ModelID: "rai.running_ability_estimate", Version: "v1", Schema: "rai.running_ability_estimate/1", ArtifactKey: "running_ability_estimate", JobKind: StackJobKind, ConfigHash: digest("vo2-product-gates;f60-prior.85/.75-.92;point-vo2-width.20-or-propagated.25;range-propagated.45;no-gamma;source-selection/v1"), SourcePolicy: "rai-vo2-only;independent-branches", ConsumerSchema: "rai.running_ability_estimate/1"}
	m.LoadFocus = &Component{Name: "load_focus", ModelID: "rai.load_focus", Version: "v1", Schema: "rai.load_focus/1", ArtifactKey: "load_focus", JobKind: StackJobKind, ConfigHash: digest("hrr-proxy.70;short-repeat3x20-120s;pre-v60x1.15;native5s;measured-grade.03;unclassified-gap;local-days/v1"), SourcePolicy: "recorded-running;no-plan-labels", ConsumerSchema: "rai.load_focus/1"}
	return m
}

// Current is the running-fitness v1 / recovery v4 release (schema4). The retired v60
// running ability and its VO2-derived estimate are gone from the stack: the
// RunningAbility slot now names the running-fitness component, and the
// load, focus and features configurations changed with it. The config hash
// strings are fixed design identities, except running fitness and the
// feature policy, which come from their pure modules.
func Current() Manifest {
	m := schema3Manifest()
	m.ProductSchema = ProductSchema4
	m.MappingVersion = MappingVersion4
	m.ConsumerVersions = []string{"ios/4", "training-adapter/4", "http/v2"}
	m.AbilityEstimate = nil
	m.Features.ConfigHash = digest("features:normalized-workout/v1;structure-ten-second-native5s-grade03/v1;" + wf.RunFitnessPolicy())
	m.Load.ConfigHash = digest("load:tiers.98-30s/.80-180s;hrr-clamp-0-1;summary.35;ratio1;horizon180;reference-v2;overlap-knownness;workout-isolated;hrref-latest-effective-no-expiry;no-pace-approximation/v4")
	m.RunningAbility = Component{Name: "running_fitness", ModelID: "rai.running_fitness", Version: "v1", Schema: "rai.running_fitness/1", InputSchema: "rai.running_fitness.input/1", JobKind: StackJobKind, ArtifactKey: "running_fitness",
		ConfigHash: rf.ConfigHash(), SourcePolicy: "recorded-running;stable-source-identity;hr-reference-at-event;effort-labels-known-at/v1", AnnotationMode: AnnotationPolicy, ConsumerSchema: "rai.running_fitness/1"}
	m.LoadFocus.ConfigHash = digest("hrr-proxy.70;short-repeat3x20-120s;pre-workout-v6x1.0;recovery-v6x.78;native5s;measured-grade.03;unclassified-gap;local-days/v2")
	m.Recovery.Version = "v4"
	m.Recovery.Schema, m.Recovery.ConsumerSchema = rc.SchemaV4, rc.SchemaV4
	m.Recovery.InputSchema = rc.InputSchemaV4
	m.Recovery.ConfigHash = digest(rc.PolicyV4)
	m.Recovery.SourcePolicy = "healthkit-readonly;night-context;latest-valid-observations;typed-rhr;explicit-model-defaults/v4"
	m.Readiness.SourcePolicy = "recorded-running;load-v2;recovery-v4-complete-or-estimated;decayed-unknown/v4"
	m.Readiness.ConfigHash = digest(rd.PolicyV4)
	return m
}

func (m Manifest) Validate() error {
	if m.ProductSchema == "rai.athlete_model/3" && (m.SessionRecovery == nil || m.AbilityEstimate == nil || m.LoadFocus == nil) {
		return ErrInvalid
	}
	if m.ProductSchema == ProductSchema4 && (m.SessionRecovery == nil || m.AbilityEstimate != nil || m.LoadFocus == nil || m.RunningAbility.ModelID != "rai.running_fitness") {
		return ErrInvalid
	}
	all := []Component{m.Features, m.Load, m.LoadState, m.RunningAbility, m.VO2Max, m.Recovery, m.Readiness, m.ExternalVO2}
	all = append(all, m.Diagnostics...)
	for _, c := range []*Component{m.SessionRecovery, m.AbilityEstimate, m.LoadFocus} {
		if c != nil {
			all = append(all, *c)
		}
	}
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
	if m.Schema != Schema || (m.ProductSchema != ProductSchema || m.MappingVersion != MappingVersion) && (m.ProductSchema != "rai.athlete_model/3" || m.MappingVersion != "rai.product_metric_mapping/3") &&
		(m.ProductSchema != ProductSchema4 || m.MappingVersion != MappingVersion4) || len(m.Diagnostics) != 4 || m.PhysiologicallyValidated {
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
