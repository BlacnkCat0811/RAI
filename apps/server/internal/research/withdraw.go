package research

import "time"

// Withdraw marks every prediction belonging to athleteKeys as withdrawn at the
// supplied time and returns a defensive copy of the snapshot plus the number of
// newly withdrawn predictions. Predictions are retained in place: history is
// never deleted, and an earlier WithdrawnAt is never overwritten by a later
// withdrawal call.
func Withdraw(snapshot ProspectiveSnapshot, athleteKeys []string, at time.Time) (ProspectiveSnapshot, int) {
	targets := make(map[string]struct{}, len(athleteKeys))
	for _, key := range athleteKeys {
		targets[key] = struct{}{}
	}

	out := cloneSnapshot(snapshot)
	withdrawn := 0
	for i := range out.Observations {
		observation := &out.Observations[i]
		if _, ok := targets[observation.AthleteKey]; !ok {
			continue
		}
		if observation.WithdrawnAt != nil || observation.State == PredictionStateWithdrawn {
			continue
		}
		withdrawnAt := at
		observation.WithdrawnAt = &withdrawnAt
		observation.State = PredictionStateWithdrawn
		withdrawn++
	}
	return out, withdrawn
}
