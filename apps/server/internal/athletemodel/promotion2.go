package athletemodel

import (
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

var ErrGateClosed = errors.New("promotion_gate_closed")

// PromotionGate is evidence bound to one exact candidate identity. Booleans
// without report digests are not accepted by the admin executor.
type PromotionGate struct {
	Model               string `json:"model"`
	Version             string `json:"version"`
	ManifestSHA         string `json:"manifestSHA256"`
	ConfigSHA           string `json:"configSHA256"`
	FeatureSchema       string `json:"featureSchema"`
	SourcePolicy        string `json:"sourcePolicy"`
	InputRegistry       string `json:"inputRegistry"`
	MappingVersion      string `json:"mappingVersion"`
	ConsumerSchema      string `json:"consumerSchema"`
	DatasetID           string `json:"datasetID"`
	SyntheticDataset    bool   `json:"syntheticDataset"`
	EngineeringReport   string `json:"engineeringReportSHA256"`
	DeviceDataReport    string `json:"deviceDataReportSHA256"`
	ApplicabilityReport string `json:"applicabilityReportSHA256"`
	UXPrivacyReport     string `json:"uxPrivacyPerformanceReportSHA256"`
	RollbackReport      string `json:"rollbackReportSHA256"`
	Authorization       string `json:"authorizationSHA256"`
	Reviewer            string `json:"reviewer"`
	ExpiresAt           string `json:"expiresAt"`
}

func (g PromotionGate) Validate(now time.Time, manifestSHA string, synthetic bool) error {
	if g.Model == "" || g.Version == "" || g.Reviewer == "" || g.ManifestSHA != manifestSHA || !validDigest(g.ConfigSHA) || !validDigest(manifestSHA) || g.FeatureSchema == "" || g.SourcePolicy == "" || g.InputRegistry == "" || g.MappingVersion != MappingVersion2 || g.ConsumerSchema != Schema2 || g.DatasetID == "" {
		return ErrGateClosed
	}
	for _, s := range []string{g.EngineeringReport, g.DeviceDataReport, g.ApplicabilityReport, g.UXPrivacyReport, g.RollbackReport, g.Authorization} {
		if !validDigest(s) {
			return ErrGateClosed
		}
	}
	// Synthetic evidence can never authorize a real dataset, and real evidence
	// is not required to run the synthetic promotion harness.
	if !synthetic {
		return ErrGateClosed
	}
	if g.SyntheticDataset != synthetic {
		return ErrGateClosed
	}
	t, err := time.Parse(time.RFC3339, g.ExpiresAt)
	if err != nil || !t.After(now) {
		return ErrGateClosed
	}
	return nil
}

func validDigest(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
