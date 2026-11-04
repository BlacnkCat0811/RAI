package athletemodel

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	pf "rai/server/internal/performance"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	tl "rai/server/internal/trainingload"
	ve "rai/server/internal/vo2maxexternal"
)

func syntheticSnapshot() AthleteModelSnapshot {
	const at int64 = 1793757600000000
	s := NewSnapshot(at, 17, "Asia/Shanghai", "2026-11-04", Assignment{"candidate", "synthetic", 1})
	a := pf.Result{Schema: pf.Schema, Model: pf.AbilityModel, Version: "v1", ConfigHash: pf.ConfigHash(), AsOf: at - 86400e6, Status: "available", Freshness: "fresh", Stability: "stable", EvidenceMode: "demonstrated", Confidence: .4, Support: pf.Support{IndependentWorkouts: 6}, Ability: &pf.Ability{Score: 55, V60: 3.3, Gamma: .9, Retention: .88}}
	s.RunningAbility = ProjectAbility(a, a.AsOf, a.AsOf, at)
	v := ve.Result{Schema: ve.Schema, ModelVersion: ve.Model, AsOf: at - 12*86400e6, Value: number(42), Unit: ve.Unit, Provider: "Apple Health", Status: "available", Quality: "complete", Selected: &ve.Selected{End: at - 12*86400e6}}
	s.ExternalVO2Max = ProjectVO2(v, v.AsOf, at)
	fresh := rc.Freshness{State: "fresh"}
	r := rc.Result{Schema: rc.Schema, ModelVersion: rc.Model, AsOf: at - 3*3600e6, Status: "available", Quality: "complete", BaselineState: "established", Score: number(82), Confidence: .45, SDNNFreshness: fresh, RHRFreshness: fresh, SleepFreshness: fresh}
	s.PhysiologicalRecovery = ProjectRecovery(r, r.AsOf, at)
	d := rd.Result{Schema: rd.Schema, ModelVersion: rd.Model, AsOf: at - 3600e6, Status: "available", Quality: "complete", Score: number(54), RecoveryScore: number(82), Penalty: number(.66), Burden: number(60), Confidence: .4}
	s.TrainingReadiness = ProjectReadiness(d, d.AsOf, at)
	l := tl.Result{Status: "available", Quality: "complete", Load: number(70)}
	day := tl.DailyState{Quality: "complete", Acute: tl.KernelValue{Value: number(35)}, Chronic: tl.KernelValue{Value: number(30)}, Balance: number(-5), Trend7: number(2)}
	wa := at - 7200e6
	s.TrainingLoad = ProjectLoad(&l, &day, &wa, at-1800e6, at-1800e6, at)
	return s
}
func goldenSnapshots() map[string]AthleteModelSnapshot {
	full := syntheticSnapshot()
	at := full.EvaluationAt
	newUser := NewSnapshot(at, 0, full.Timezone, full.CalendarDate, full.Assignment)
	loadOnly := newUser
	loadOnly.TrainingLoad = full.TrainingLoad
	abilityOnly := newUser
	abilityOnly.RunningAbility = full.RunningAbility
	collecting := full
	collecting.PhysiologicalRecovery = Missing[RecoveryValue]("physiologicalRecovery", at, "collecting_baseline")
	mixed := full
	mixed.RunningAbility = newUser.RunningAbility
	mixed.ExternalVO2Max = newUser.ExternalVO2Max
	stale := full
	stale.ExternalVO2Max = ProjectVO2(ve.Result{Schema: ve.Schema, ModelVersion: ve.Model, Status: "stale", Quality: "complete", Value: number(42), Unit: ve.Unit, Provider: "Apple Health", AsOf: at - 40*86400e6}, at-40*86400e6, at)
	expired := full
	expired.RunningAbility = Missing[RunningAbilityValue]("runningAbility", at, "expired")
	expired.RunningAbility.Envelope.ModelVersion = pf.AbilityModel + "/v1"
	expired.RunningAbility.Envelope.Freshness = "expired"
	missing := full
	missing.PhysiologicalRecovery = Missing[RecoveryValue]("physiologicalRecovery", at, "insufficient_data")
	readiness := full
	readiness.TrainingReadiness = Missing[ReadinessValue]("trainingReadiness", at, "sync_incomplete")
	future := full
	future.RunningAbility.Envelope.ModelVersion = "rai.running_ability/v99"
	future.RunningAbility.Envelope.State = Limited
	future.RunningAbility.Envelope.ModelStatus = "future_status"
	future.RunningAbility.Envelope.Quality = "future_quality"
	future.RunningAbility.Envelope.ReasonCodes = []string{"future_reason"}
	future.RunningAbility.Envelope.ProductReasonCodes = []ProductReasonCode{"future_product_reason"}
	return map[string]AthleteModelSnapshot{"full": full, "mixed": mixed, "new_user": newUser, "load_only": loadOnly, "ability_only": abilityOnly, "recovery_collecting": collecting, "stale_vo2": stale, "expired_ability": expired, "missing_recovery": missing, "readiness_unavailable": readiness, "future_unknown": future}
}
func TestGoldenProductSnapshots(t *testing.T) {
	for name, s := range goldenSnapshots() {
		t.Run(name, func(t *testing.T) {
			b, e := json.MarshalIndent(s, "", "  ")
			if e != nil {
				t.Fatal(e)
			}
			b = append(b, '\n')
			for _, path := range []string{"../../../../contracts/fixtures/athlete-model", "../../../../ios/RAI/Tests/ProductCoreTests/Fixtures/AthleteModel"} {
				p := filepath.Join(path, name+".json")
				if os.Getenv("RAI_UPDATE_M7_GOLDENS") == "1" {
					if e = os.MkdirAll(path, 0755); e != nil {
						t.Fatal(e)
					}
					if e = os.WriteFile(p, b, 0644); e != nil {
						t.Fatal(e)
					}
				}
				want, e := os.ReadFile(p)
				if e != nil {
					t.Fatal(e)
				}
				if !bytes.Equal(b, want) {
					t.Fatal("golden mismatch", name)
				}
			}
			var decoded AthleteModelSnapshot
			if e = json.Unmarshal(b, &decoded); e != nil {
				t.Fatal(e)
			}
			again, _ := json.MarshalIndent(decoded, "", "  ")
			if !bytes.Equal(bytes.TrimSpace(b), again) {
				t.Fatal("semantic round trip")
			}
		})
	}
}
func TestProductMappingProperties(t *testing.T) {
	codes := MapReasons("insufficient_data", []string{"missing_hr", "recent_load_unknown"})
	if !reflect.DeepEqual(codes, []ProductReasonCode{"insufficient_evidence", "missing_heart_rate", "training_load_unknown"}) {
		t.Fatal("current model reason mapping loses missingness", codes)
	}
	for _, tc := range []struct {
		status, fresh string
		state         ProductMetricState
	}{{"available", "fresh", Available}, {"partial", "fresh", Limited}, {"collecting_baseline", "unknown", Collecting}, {"source_selection_required", "unknown", Unavailable}, {"sync_incomplete", "unknown", Unavailable}, {"expired", "expired", Stale}, {"future", "unknown", Unavailable}, {"unsupported", "fresh", Limited}} {
		if got := MapProductState(tc.status, tc.fresh, true); got != tc.state {
			t.Fatal(tc, got)
		}
	}
	reasons := []string{"source_conflict", "input_stale", "sync_incomplete", "source_conflict"}
	want := MapReasons("partial", reasons)
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 1000; i++ {
		rng.Shuffle(len(reasons), func(a, b int) { reasons[a], reasons[b] = reasons[b], reasons[a] })
		if !reflect.DeepEqual(want, MapReasons("partial", reasons)) {
			t.Fatal("reason order nondeterministic")
		}
	}
	for _, s := range goldenSnapshots() {
		first, _ := json.Marshal(s)
		var fields map[string]json.RawMessage
		if e := json.Unmarshal(first, &fields); e != nil {
			t.Fatal(e)
		}
		keys := []string{}
		for key := range fields {
			keys = append(keys, key)
		}
		for range 100 {
			rng.Shuffle(len(keys), func(a, b int) { keys[a], keys[b] = keys[b], keys[a] })
			var shuffled bytes.Buffer
			shuffled.WriteByte('{')
			for i, key := range keys {
				if i > 0 {
					shuffled.WriteByte(',')
				}
				name, _ := json.Marshal(key)
				shuffled.Write(name)
				shuffled.WriteByte(':')
				shuffled.Write(fields[key])
			}
			shuffled.WriteByte('}')
			var decoded AthleteModelSnapshot
			if e := json.Unmarshal(shuffled.Bytes(), &decoded); e != nil {
				t.Fatal(e)
			}
			second, _ := json.Marshal(decoded)
			if !bytes.Equal(first, second) {
				t.Fatal("input metric order changed canonical snapshot")
			}
		}
	}
	s := NewSnapshot(1, 0, "UTC", "2026-11-04", Assignment{Channel: "candidate"})
	if s.TrainingLoad.Value != nil || s.TrainingReadiness.Value != nil || s.RunningAbility.Value != nil {
		t.Fatal("new user fabricated values")
	}
}
func TestTrendAndPromotionGates(t *testing.T) {
	deviation := &rc.Deviation{Raw: 2, Clipped: 2, Burden: .5}
	recovery := ProjectRecovery(rc.Result{SDNNDeviation: deviation, RHRDeviation: deviation, SleepDeviation: deviation}, 1, 1)
	if recovery.Value.HRV.RelativeToBaseline != "below_baseline" || recovery.Value.RHR.RelativeToBaseline != "above_baseline" || recovery.Value.Sleep.RelativeToBaseline != "below_baseline" {
		t.Fatal("recovery explanation reversed model deviation direction")
	}
	if component(rc.Freshness{}, &rc.Deviation{Raw: -2}, "above_baseline").RelativeToBaseline != "no_adverse_deviation" {
		t.Fatal("explanation invented adverse evidence")
	}
	partial := tl.DailyState{Quality: "partial", Acute: tl.KernelValue{Value: number(1)}, Chronic: tl.KernelValue{Value: number(1)}}
	if ProjectLoad(nil, &partial, nil, 1, 1, 1).Envelope.State != Limited {
		t.Fatal("partial load presented as complete")
	}
	a := pf.Result{Model: pf.AbilityModel, Version: "v1", Status: "available", Freshness: "fresh", EvidenceMode: "demonstrated", Ability: &pf.Ability{Score: 50}}
	b := a
	if RunningAbilityTrend(a, b) == nil {
		t.Fatal("comparable")
	}
	b.Version = "v2"
	if RunningAbilityTrend(a, b) != nil {
		t.Fatal("mixed model trend")
	}
	b = a
	b.EvidenceMode = "other"
	if RunningAbilityTrend(a, b) != nil {
		t.Fatal("mixed mode trend")
	}
	r := rc.Result{ModelVersion: rc.Model, Date: "2026-11-04", Status: "available", Quality: "complete", Score: number(50)}
	p := r
	p.Date = "2026-11-03"
	if RecoveryDailyTrend(r, p) == nil {
		t.Fatal("daily comparable")
	}
	p.Status = "partial"
	if RecoveryDailyTrend(r, p) != nil {
		t.Fatal("partial daily trend")
	}
	for _, g := range PromotionPlan() {
		if g.Promotable || g.RealDeviceAccepted || g.PhysiologicalValidated || g.ProductContractAccepted || g.RollbackReady || len(g.BlockingReasons) == 0 {
			t.Fatal("promotion opened")
		}
	}
	if !Acyclic(DependencyDAG()) || Acyclic(append(DependencyDAG(), Edge{"M6 Readiness", "M1 Features"})) {
		t.Fatal("cycle detection")
	}
}
func BenchmarkProductProjection(b *testing.B) {
	s := syntheticSnapshot()
	b.ReportAllocs()
	for b.Loop() {
		_ = ProjectAbility(pf.Result{Model: pf.AbilityModel, Version: "v1", Status: "collecting"}, s.AsOf, s.AsOf, s.AsOf)
	}
}
func BenchmarkProductSerialization(b *testing.B) {
	for name, s := range goldenSnapshots() {
		if name != "full" && name != "mixed" && name != "new_user" && name != "stale_vo2" {
			continue
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, e := json.Marshal(s); e != nil {
					b.Fatal(e)
				}
			}
		})
	}
}
