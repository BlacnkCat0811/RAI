package athletemodel

type ModelPromotionGate struct {
	ModelVersion            string   `json:"modelVersion"`
	EngineeringComplete     bool     `json:"engineeringComplete"`
	RealDeviceAccepted      bool     `json:"realDeviceAccepted"`
	PhysiologicalValidated  bool     `json:"physiologicalValidated"`
	ProductContractAccepted bool     `json:"productContractAccepted"`
	RollbackReady           bool     `json:"rollbackReady"`
	Promotable              bool     `json:"promotable"`
	BlockingReasons         []string `json:"blockingReasons"`
	RequiredAcceptance      []string `json:"requiredAcceptance"`
	RollbackPlan            []string `json:"rollbackPlan"`
}

// Engineering completion cannot activate a product. This read-only plan never
// writes assignment, promotes a channel or grants activation permission.
func PromotionPlan() []ModelPromotionGate {
	models := []struct {
		model      string
		acceptance []string
	}{
		{"rai.training_load.hrr_integral/v1", []string{"real_hr_coverage", "hr_parameter_validity"}},
		{"rai.running_ability/v1", []string{"real_performance_eligibility", "prediction_calibration_evaluation"}},
		{"rai.vo2max.external_projection/v1", []string{"real_device_source_metadata", "external_estimate_semantics_no_lab_claim"}},
		{"rai.recovery.physiological/v1", []string{"longitudinal_recovery_calibration"}},
		{"rai.readiness.training/v1", []string{"longitudinal_readiness_validation", "real_running_load_completeness"}},
	}
	out := []ModelPromotionGate{}
	for _, m := range models {
		out = append(out, ModelPromotionGate{ModelVersion: m.model, EngineeringComplete: true, BlockingReasons: []string{"real_device_acceptance_deferred", "model_acceptance_not_verified", "product_activation_not_authorized", "rollback_exercise_not_verified"}, RequiredAcceptance: append([]string{"engineering", "real_device_data", "model_specific_acceptance", "product_semantics_accessibility_privacy_performance", "rollback_exercise"}, m.acceptance...), RollbackPlan: []string{"review_new_assignment_revision_to_previous_active", "retain_candidate_and_versioned_history", "never_mutate_raw_facts", "current_database_constraint_keeps_active_legacy"}})
	}
	return out
}

type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func DependencyDAG() []Edge {
	return []Edge{{"Workout Raw", "M1 Features"}, {"M1 Features", "M2 Training Load"}, {"M1 Features", "M3 Running Ability"}, {"Daily Health", "M5B Recovery"}, {"M2 Training Load", "M6 Readiness"}, {"M5B Recovery", "M6 Readiness"}, {"VO2 Raw", "M4 External VO2max"}}
}
func Acyclic(edges []Edge) bool {
	adj := map[string][]string{}
	state := map[string]int{}
	for _, e := range edges {
		adj[e.From] = append(adj[e.From], e.To)
	}
	var visit func(string) bool
	visit = func(n string) bool {
		if state[n] == 1 {
			return false
		}
		if state[n] == 2 {
			return true
		}
		state[n] = 1
		for _, to := range adj[n] {
			if !visit(to) {
				return false
			}
		}
		state[n] = 2
		return true
	}
	for n := range adj {
		if !visit(n) {
			return false
		}
	}
	return true
}
