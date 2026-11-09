package athletemodel

import (
	"errors"
	"strings"
	"time"
)

const (
	OwnerAuthorizationSchema = "rai.owner_activation_authorization/1"
	OwnerAuthorizationScope  = "real_dataset_product_activation"
	OwnerAuthorizationPhrase = "I authorize activation of the managed athlete model stack on this dataset and accept that model outputs are unvalidated and may be unavailable."
)

var ErrOwnerAuthorizationInvalid = errors.New("owner_activation_authorization_invalid")

// OwnerAuthorization is an explicit, dataset-bound override for the synthetic
// promotion harness. It never represents passed device, calibration or UX
// gates; the audit record keeps that distinction.
type OwnerAuthorization struct {
	Schema         string `json:"schema"`
	DatasetID      string `json:"datasetID"`
	StackID        string `json:"stackID"`
	ManifestSHA256 string `json:"manifestSHA256"`
	Scope          string `json:"scope"`
	AuthorizedBy   string `json:"authorizedBy"`
	AuthorizedAt   string `json:"authorizedAt"`
	ExpiresAt      string `json:"expiresAt"`
	Statement      string `json:"statement"`
}

func (a OwnerAuthorization) Validate(now time.Time, dataset, stack, manifest string) error {
	if a.Schema != OwnerAuthorizationSchema || a.DatasetID != dataset || a.StackID != stack ||
		a.ManifestSHA256 != manifest || a.Scope != OwnerAuthorizationScope ||
		strings.TrimSpace(a.AuthorizedBy) == "" || a.Statement != OwnerAuthorizationPhrase {
		return ErrOwnerAuthorizationInvalid
	}
	from, err := time.Parse(time.RFC3339, a.AuthorizedAt)
	if err != nil {
		return ErrOwnerAuthorizationInvalid
	}
	until, err := time.Parse(time.RFC3339, a.ExpiresAt)
	if err != nil || !until.After(from) || !until.After(now) || from.After(now.Add(5*time.Minute)) {
		return ErrOwnerAuthorizationInvalid
	}
	return nil
}
