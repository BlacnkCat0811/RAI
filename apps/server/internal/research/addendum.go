package research

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// ErrAddendumInvalid marks a study addendum whose gate must stay closed.
var ErrAddendumInvalid = errors.New("research: study addendum invalid")

// ValidateAddendum checks that a per-model study addendum is fully specified,
// pre-registered and privacy-approved. It returns true only when the gate may
// open; any missing or inconsistent field returns false and an error wrapping
// ErrAddendumInvalid. Expiry ordering is validated here; whether the addendum
// has expired at execution time remains a caller-visible field.
func ValidateAddendum(addendum StudyAddendum) (bool, error) {
	var problems []string
	add := func(condition bool, message string) {
		if condition {
			problems = append(problems, message)
		}
	}

	add(strings.TrimSpace(addendum.CohortID) == "", "cohort is required")
	add(strings.TrimSpace(addendum.Purpose) == "", "purpose is required")
	add(strings.TrimSpace(addendum.PrimaryOutcome) == "", "primary outcome is required")
	add(strings.TrimSpace(addendum.OutcomeSource) == "", "outcome source is required")
	add(addendum.ObservationFrom.IsZero() || addendum.ObservationTo.IsZero() ||
		!addendum.ObservationTo.After(addendum.ObservationFrom), "observation window must have observationTo after observationFrom")
	add(math.IsNaN(addendum.AcceptanceThreshold) || math.IsInf(addendum.AcceptanceThreshold, 0) ||
		addendum.AcceptanceThreshold <= 0, "acceptance threshold must be positive")
	add(strings.TrimSpace(addendum.SampleSizeJustification) == "", "sample size justification is required")
	add(math.IsNaN(addendum.MinDetectableEffect) || math.IsInf(addendum.MinDetectableEffect, 0) ||
		addendum.MinDetectableEffect <= 0, "minimum detectable effect must be positive")
	add(!validProbability(addendum.Alpha), "alpha must be within (0,1)")
	add(!validProbability(addendum.Power), "power must be within (0,1)")
	add(strings.TrimSpace(addendum.Reviewer) == "", "reviewer is required")
	add(strings.TrimSpace(addendum.AuthorizationBasis) == "", "authorization basis is required")
	add(strings.TrimSpace(addendum.WithdrawalProcedure) == "", "withdrawal procedure is required")
	add(strings.TrimSpace(addendum.StoppingRule) == "", "stopping rule is required")
	add(addendum.PreregisteredAt.IsZero(), "preregisteredAt is required")
	add(!addendum.PrivacyApproved, "privacy approval is required")
	add(addendum.ExpiresAt.IsZero(), "expiresAt is required")
	add(!addendum.PreregisteredAt.IsZero() && !addendum.ExpiresAt.IsZero() &&
		!addendum.ExpiresAt.After(addendum.PreregisteredAt), "expiresAt must be after preregisteredAt")
	add(!validModelIdentity(addendum.Model), "model must be a rai.* identity")
	add(strings.TrimSpace(addendum.Version) == "", "version is required")

	if len(problems) > 0 {
		return false, fmt.Errorf("%w: %s", ErrAddendumInvalid, strings.Join(problems, "; "))
	}
	return true, nil
}

func validProbability(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0 && value < 1
}

func validModelIdentity(model string) bool {
	model = strings.TrimSpace(model)
	return strings.HasPrefix(model, "rai.") && len(model) > len("rai.")
}
