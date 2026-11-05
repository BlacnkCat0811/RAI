package syncing

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// RAI Workout Source Policy V1 (UA1-R3B).
//
// The product decision is frozen: only an Apple Watch native running Workout is
// part of the RAI data domain. An explicitly identified third-party or
// user-entered Workout is excluded by policy and must be proven excluded, not
// silently skipped. Anything the frozen classifier cannot resolve is
// unclassified and blocks completeness instead of being folded into excluded.
const (
	WorkoutSourcePolicyVersion       = "rai.workout.source_policy.apple_watch_only/v1"
	WorkoutSourcePolicySHA256        = "63713b4f82a33c6e9544de37fbd0701c42a29b71f2ca54588c7fc9cf463e0654"
	WorkoutSourceClassificationVer   = 1
	WorkoutSourcePolicyEvidenceBytes = 2048
)

// Outcomes are the only three states an inventory entry can reach.
const (
	SourceOutcomeEligible     = "eligible"
	SourceOutcomeExcluded     = "excluded"
	SourceOutcomeUnclassified = "unclassified"
)

// Reasons are stable public vocabulary; they carry no source identifiers.
const (
	SourceReasonEligible     = "eligible_source_policy"
	SourceReasonExcluded     = "excluded_by_source_policy"
	SourceReasonUnclassified = "unclassified_source"
)

// sourcePolicyOutcomes is the whole policy. A classification missing from this
// table can never become eligible or excluded by accident.
var sourcePolicyOutcomes = map[string]string{
	"apple_watch":            SourceOutcomeEligible,
	"third_party":            SourceOutcomeExcluded,
	"user_entered":           SourceOutcomeExcluded,
	"associated_apple_watch": SourceOutcomeUnclassified,
	"source_unknown":         SourceOutcomeUnclassified,
}

func SourcePolicyOutcome(classification string) string {
	if outcome, ok := sourcePolicyOutcomes[classification]; ok {
		return outcome
	}
	return SourceOutcomeUnclassified
}

func SourcePolicyReason(outcome string) string {
	switch outcome {
	case SourceOutcomeEligible:
		return SourceReasonEligible
	case SourceOutcomeExcluded:
		return SourceReasonExcluded
	}
	return SourceReasonUnclassified
}

// SourcePolicyDevice mirrors the minimal HealthKit device descriptor used by
// the frozen classifier. Empty strings mean "not reported".
type SourcePolicyDevice struct {
	Manufacturer    string `json:"manufacturer"`
	Model           string `json:"model"`
	HardwareVersion string `json:"hardwareVersion"`
	FirmwareVersion string `json:"firmwareVersion"`
	SoftwareVersion string `json:"softwareVersion"`
}

// SourcePolicyEvidence is the canonical, minimal source evidence a client may
// upload to justify an exclusion. It is exactly the descriptor the frozen
// classifier reads, and nothing else.
type SourcePolicyEvidence struct {
	BundleID               string             `json:"bundleID"`
	Name                   string             `json:"name"`
	Version                string             `json:"version"`
	ProductType            string             `json:"productType"`
	OperatingSystemVersion string             `json:"operatingSystemVersion"`
	Device                 SourcePolicyDevice `json:"device"`
}

// Source rebuilds the HealthKit source descriptor the classifier consumes.
func (e SourcePolicyEvidence) Source() HealthKitSource {
	s := HealthKitSource{BundleID: e.BundleID, Name: e.Name, Version: e.Version,
		ProductType: e.ProductType, OperatingSystemVersion: e.OperatingSystemVersion}
	if e.Device != (SourcePolicyDevice{}) {
		s.Device = &HealthKitDevice{Manufacturer: e.Device.Manufacturer, Model: e.Device.Model,
			HardwareVersion: e.Device.HardwareVersion, FirmwareVersion: e.Device.FirmwareVersion,
			SoftwareVersion: e.Device.SoftwareVersion}
	}
	return s
}

// Classification re-runs the frozen policy on the submitted evidence. The
// server never accepts a client-declared classification without this.
func (e SourcePolicyEvidence) Classification() string { return e.Source().Classification(nil) }

func (e SourcePolicyEvidence) Canonical() []byte { return completenessCanonical(e) }

func (e SourcePolicyEvidence) SHA256() string { return BundleDigest(e.Canonical()) }

func (e SourcePolicyEvidence) Validate() error {
	if len(e.Canonical()) > WorkoutSourcePolicyEvidenceBytes {
		return Fail(422, "invalid_source_evidence")
	}
	for _, value := range []string{e.BundleID, e.Name, e.Version, e.ProductType, e.OperatingSystemVersion,
		e.Device.Manufacturer, e.Device.Model, e.Device.HardwareVersion, e.Device.FirmwareVersion, e.Device.SoftwareVersion} {
		if len(value) > 255 {
			return Fail(422, "invalid_source_evidence")
		}
	}
	return nil
}

// WorkoutIdentitySHA256 is the only Workout identity that may appear in
// exclusion evidence. The raw Workout UUID never leaves the protected channel
// for an excluded Workout.
func WorkoutIdentitySHA256(workoutID string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(workoutID)))
	return hex.EncodeToString(sum[:])
}

// InventorySHA256 commits to a bounded inventory membership set. Callers pass
// identity hashes; ordering is normalised so a permutation cannot change the
// manifest identity.
func InventorySHA256(identityHashes []string) string {
	sorted := append([]string{}, identityHashes...)
	sort.Strings(sorted)
	return BundleDigest(completenessCanonical(sorted))
}
