// Package modelrelease preserves the read contracts of supported published
// releases. It never executes an old model or relabels it as the current one.
package modelrelease

import (
	_ "embed"
	"encoding/json"

	"rai/server/internal/modelmanifest"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
)

type Release struct {
	Manifest          modelmanifest.Manifest `json:"manifest"`
	LoadConfigHash    string                 `json:"loadConfigHash"`
	AbilityConfigHash string                 `json:"abilityConfigHash"`
	RecoveryConfig    rc.Config              `json:"recoveryConfig"`
	ReadinessConfig   rd.Config              `json:"readinessConfig"`
}

// Frozen directly from caca7f4, 975b912, ccbe328 and the recovery v2.3
// evaluation-day tombstone release. Add a release when its manifest changes;
// never rewrite an existing entry to match newer code.
//
//go:embed releases.json
var frozen []byte

var releases = func() []Release {
	var rs []Release
	if err := json.Unmarshal(frozen, &rs); err != nil {
		panic(err)
	}
	for _, r := range rs {
		if r.Manifest.Validate() != nil || len(r.LoadConfigHash) != 64 || len(r.AbilityConfigHash) != 64 {
			panic("invalid frozen model release")
		}
	}
	return rs
}()

// Lookup requires the complete assignment identity, not just its short stack
// name. Timezone is the dataset's calendar, also bound by artifact validation.
func Lookup(stack, manifest, timezone string) (Release, bool) {
	for _, r := range releases {
		h, err := modelmanifest.Hash(r.Manifest)
		if err == nil && h == manifest && stack == "rai-v2-"+h[:16] {
			r.Manifest.Diagnostics = append([]modelmanifest.Component(nil), r.Manifest.Diagnostics...)
			r.Manifest.ConsumerVersions = append([]string(nil), r.Manifest.ConsumerVersions...)
			r.RecoveryConfig.Timezone = timezone
			r.ReadinessConfig.Timezone = timezone
			return r, true
		}
	}
	return Release{}, false
}
