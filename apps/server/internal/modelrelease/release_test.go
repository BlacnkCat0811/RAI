package modelrelease

import (
	"reflect"
	"testing"

	"rai/server/internal/modelmanifest"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	rf "rai/server/internal/runfitness"
	sr "rai/server/internal/sessionrecovery"
	tl "rai/server/internal/trainingload"
)

func TestCurrentReleaseIsFrozenWithItsActualCalculationConfigs(t *testing.T) {
	h, _ := modelmanifest.CurrentHash()
	r, ok := Lookup(modelmanifest.StackID(), h, "Asia/Shanghai")
	if !ok || !reflect.DeepEqual(r.Manifest, modelmanifest.Current()) || r.LoadConfigHash != tl.ConfigHashV2() ||
		r.AbilityConfigHash != rf.ConfigHash() || r.RecoveryConfig != rc.DefaultV3Config("Asia/Shanghai") || r.ReadinessConfig != rd.DefaultV4Config("Asia/Shanghai") {
		t.Fatal("register the changed manifest and calculation configs as a new immutable release")
	}
	if r.Manifest.SessionRecovery == nil || r.Manifest.SessionRecovery.ConfigHash != sr.ConfigHash() || r.Manifest.AbilityEstimate != nil || r.Manifest.RunningAbility.ConfigHash != rf.ConfigHash() || r.Manifest.LoadFocus == nil || r.Manifest.LoadFocus.ConfigHash != tl.FocusConfigHash() {
		t.Fatal("new pure-module configurations do not match the frozen manifest")
	}
}

func TestRetainedReleaseIdentityAndConfigAreIndependentOfCurrentCode(t *testing.T) {
	seen := map[string]bool{}
	for _, frozen := range releases {
		h, _ := modelmanifest.Hash(frozen.Manifest)
		stack := "rai-v2-" + h[:16]
		if seen[stack] {
			t.Fatal("duplicate frozen stack")
		}
		seen[stack] = true
		r, ok := Lookup(stack, h, "UTC")
		if !ok || r.RecoveryConfig.Timezone != "UTC" || r.ReadinessConfig.Timezone != "UTC" {
			t.Fatal("retained release missing")
		}
		if _, ok := Lookup(stack, h[:63]+"x", "UTC"); ok {
			t.Fatal("wrong full identity accepted")
		}
		if _, ok := Lookup(stack+"x", h, "UTC"); ok {
			t.Fatal("wrong stack accepted")
		}
	}
	if len(seen) < 3 {
		t.Fatal("read compatibility lost")
	}
}
