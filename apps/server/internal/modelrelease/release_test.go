package modelrelease

import (
	"reflect"
	"testing"

	"rai/server/internal/modelmanifest"
	pf "rai/server/internal/performance"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	tl "rai/server/internal/trainingload"
)

func TestCurrentReleaseIsFrozenWithItsActualCalculationConfigs(t *testing.T) {
	h, _ := modelmanifest.CurrentHash()
	r, ok := Lookup(modelmanifest.StackID(), h, "Asia/Shanghai")
	if !ok || !reflect.DeepEqual(r.Manifest, modelmanifest.Current()) || r.LoadConfigHash != tl.ConfigHashV2() ||
		r.AbilityConfigHash != pf.ConfigHashV2() || r.RecoveryConfig != rc.DefaultV2Config("Asia/Shanghai") || r.ReadinessConfig != rd.DefaultV3Config("Asia/Shanghai") {
		t.Fatal("register the changed manifest and calculation configs as a new immutable release")
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
