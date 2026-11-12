package postgres

import (
	"encoding/json"
	am "rai/server/internal/athletemodel"
	"rai/server/internal/modelrelease"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	sr "rai/server/internal/sessionrecovery"
	tl "rai/server/internal/trainingload"
)

func projectOptimizedArtifacts(out *am.AthleteModelSnapshot, rows map[string]publishedModelArtifact, dataset, tz string, release modelrelease.Release, at int64) {
	if a, ok := rows["recovery"]; ok {
		var r rc.V3Result
		out.PhysiologicalRecovery = am.Missing[am.RecoveryValue]("physiologicalRecovery", at, "technical_failure")
		if validHistoryArtifactForRelease("physiologicalRecovery", dataset, tz, a, release) && json.Unmarshal(a.Payload, &r) == nil {
			if v, e := rc.FreshenV3(r, at); e == nil {
				out.PhysiologicalRecovery = am.ProjectRecoveryV3(v, a.PublishedAt.UnixMicro(), at)
				stampArtifact(&out.PhysiologicalRecovery, a)
			}
		}
	}
	if a, ok := rows["readiness"]; ok {
		var r rd.V4Result
		out.TrainingReadiness = am.Missing[am.ReadinessValue]("trainingReadiness", at, "technical_failure")
		if validHistoryArtifactForRelease("trainingReadiness", dataset, tz, a, release) && json.Unmarshal(a.Payload, &r) == nil {
			if v, e := rd.FreshenV4(r, at); e == nil {
				out.TrainingReadiness = am.ProjectReadinessV4(v, a.PublishedAt.UnixMicro(), at)
				stampArtifact(&out.TrainingReadiness, a)
			}
		}
	}
	if a, ok := rows["session"]; ok {
		var r sr.Result
		m := am.Missing[am.SessionRecoveryValue]("sessionRecovery", at, "technical_failure")
		if validHistoryArtifactForRelease("sessionRecovery", dataset, tz, a, release) && json.Unmarshal(a.Payload, &r) == nil {
			if v, e := sr.Freshen(r, at); e == nil {
				m = am.ProjectSessionRecovery(v, a.PublishedAt.UnixMicro(), at)
				stampArtifact(&m, a)
			}
		}
		out.SessionRecovery = &m
	}
	if a, ok := rows["focus"]; ok {
		var r tl.FocusResult
		m := am.Missing[am.LoadFocusValue]("loadFocus", at, "technical_failure")
		if validHistoryArtifactForRelease("loadFocus", dataset, tz, a, release) && json.Unmarshal(a.Payload, &r) == nil {
			m = am.ProjectFocus(r, a.PublishedAt.UnixMicro(), at)
			stampArtifact(&m, a)
		}
		out.LoadFocus = &m
	}
}
