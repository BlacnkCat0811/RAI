package training

import (
	"bytes"
	"encoding/json"
	"sort"
)

// ActiveOverrides has explicit precedence; database row order is irrelevant.
func ActiveOverrides(events []Override) []Override {
	xs := append([]Override(nil), events...)
	sort.Slice(xs, func(i, j int) bool {
		if xs[i].Revision == xs[j].Revision {
			return xs[i].ID < xs[j].ID
		}
		return xs[i].Revision < xs[j].Revision
	})
	revoked := map[string]Override{}
	for _, o := range xs {
		if o.Operation == "revoke" {
			revoked[o.TargetID] = o
		}
	}
	for i := range xs {
		xs[i].Status = "active"
		xs[i].UpdatedAt = xs[i].CreatedAt
		if r, ok := revoked[xs[i].ID]; ok {
			xs[i].Status = "revoked"
			xs[i].UpdatedAt = r.CreatedAt
		}
	}
	return xs
}
func ApplyField(s *Session, path string, v json.RawMessage) error {
	if len(v) == 0 {
		return ErrInvalid
	}
	nullable := path == "targetHR" || path == "paceSPerKM"
	if string(bytes.TrimSpace(v)) == "null" && !nullable {
		return ErrInvalid
	}
	var err error
	switch path {
	case "date":
		err = json.Unmarshal(v, &s.Date)
	case "type":
		old := s.Type
		err = json.Unmarshal(v, &s.Type)
		if err == nil && s.Type != "INTERVAL" {
			s.Steps = nil
		} // a changed type cannot retain interval structure
		if err == nil && s.Type != old {
			// A generated focus describes the old type only.
			s.Focus, s.Purpose = "", ""
		}
	case "durationS":
		oldDuration := s.DurationS
		err = json.Unmarshal(v, &s.DurationS)
		if err == nil && len(s.Steps) > 0 && s.DurationS != oldDuration {
			s.Steps = nil
			s.Reasons = append(s.Reasons, "duration_override_unstructured")
		}
	case "distanceM":
		err = json.Unmarshal(v, &s.DistanceM)
	case "effort":
		err = json.Unmarshal(v, &s.Effort)
	case "targetHR":
		err = json.Unmarshal(v, &s.TargetHR)
	case "paceSPerKM":
		err = json.Unmarshal(v, &s.PaceSPerKM)
	case "notes":
		err = json.Unmarshal(v, &s.Notes)
	default:
		return ErrInvalid
	}
	if err != nil {
		return ErrInvalid
	}
	return ValidateSession(*s)
}
func ResolveEffective(base Base, events []Override) (Base, error) {
	return ResolveEffectiveVersion(base, events, MergeVersion)
}

// Missing mergeVersion in immutable pre-v2 Effective JSON means v1. Explicit
// dispatch keeps its historical projection replayable without rewriting it.
func ResolveEffectiveVersion(base Base, events []Override, version string) (Base, error) {
	if version != "" && version != "training_merge/v1" && version != MergeVersion {
		return Base{}, ErrInvalid
	}
	rebase := version == MergeVersion
	b, _ := json.Marshal(base)
	var out Base
	if e := json.Unmarshal(b, &out); e != nil {
		return Base{}, e
	}
	sessions := map[string]Session{}
	for _, s := range out.Sessions {
		sessions[s.ID] = s
	}
	active := ActiveOverrides(events)
	// A rolling (v4) base keeps only a window of days. A user change to a day
	// that has left the window stays in the audit trail but is no longer part
	// of the plan; without this it would come back as a removed-base conflict.
	agedOut := map[string]bool{}
	if out.WindowStart != "" {
		last := map[string]string{}
		for _, o := range active {
			if o.Status != "active" || o.Operation == "revoke" || o.Anchor == nil {
				continue
			}
			if _, seen := last[o.SessionID]; !seen {
				last[o.SessionID] = o.Anchor.Date
			}
			var d string
			if o.Operation == "move" && json.Unmarshal(o.Value, &d) == nil && d != "" {
				last[o.SessionID] = d
			}
		}
		for id, d := range last {
			if _, ok := sessions[id]; !ok && d < out.WindowStart {
				agedOut[id] = true
			}
		}
	}
	latest := map[string]string{}
	if rebase {
		for _, o := range active {
			if o.Status == "active" && (o.Operation == "edit" || o.Operation == "move") {
				latest[o.SessionID+":"+o.Path] = o.ID
			}
		}
	}
	for _, o := range active {
		if o.Status != "active" || o.Operation == "revoke" {
			continue
		}
		if rebase && (o.Operation == "edit" || o.Operation == "move") && latest[o.SessionID+":"+o.Path] != o.ID {
			continue
		}
		if agedOut[o.SessionID] {
			continue
		}
		s, ok := sessions[o.SessionID]
		if !ok || (s.Type == "REST" && o.Anchor != nil && o.Anchor.Type != "REST") {
			if o.Anchor == nil {
				return Base{}, ErrInvalid
			}
			s = cloneSession(*o.Anchor)
			if rebase {
				// Anchor prescriptions are facts; their old projection conflicts
				// are not current conflicts. Recompute those after revoke/rebase.
				s.Conflicts = nil
				reasons := s.Reasons[:0]
				for _, reason := range s.Reasons {
					if reason != "override_rebase_conflict/v1" {
						reasons = append(reasons, reason)
					}
				}
				s.Reasons = reasons
			}
			if o.Operation != "add" {
				s.Conflicts = append(s.Conflicts, "base_session_removed_kept")
			}
		}
		switch o.Operation {
		case "add":
			if o.Anchor == nil {
				return Base{}, ErrInvalid
			}
			s = cloneSession(*o.Anchor)
			s.Origin = "user_added"
		case "edit":
			if o.Path == "date" {
				return Base{}, ErrInvalid
			}
			candidate := cloneSession(s)
			if e := ApplyField(&candidate, o.Path, o.Value); e != nil {
				// Only an originally valid fact can become a rebase conflict.
				// Keep the current legal prescription and expose the field conflict.
				if !rebase || o.Anchor == nil {
					return Base{}, e
				}
				anchor := cloneSession(*o.Anchor)
				if ApplyField(&anchor, o.Path, o.Value) != nil {
					return Base{}, e
				}
				s.Conflicts = append(s.Conflicts, o.Path+"_override_incompatible_with_session_structure/v1")
				s.Reasons = append(s.Reasons, "override_rebase_conflict/v1")
			} else {
				s = candidate
			}
		case "move":
			if o.Path != "date" {
				return Base{}, ErrInvalid
			}
			if e := ApplyField(&s, "date", o.Value); e != nil {
				return Base{}, e
			}
		case "skip":
			s.Status = "skipped"
		case "delete":
			s.Status = "cancelled"
		default:
			return Base{}, ErrInvalid
		}
		s.Modified = true
		sessions[s.ID] = s
	}
	out.Sessions = []Session{}
	for _, s := range sessions {
		out.Sessions = append(out.Sessions, s)
	}
	SortSessions(out.Sessions)
	for _, s := range out.Sessions {
		if s.Modified {
			out.Recommendations = append(out.Recommendations, "user_override_review")
			break
		}
	}
	return out, nil
}

func cloneSession(s Session) Session {
	s.Steps = append([]Step(nil), s.Steps...)
	s.Reasons = append([]string(nil), s.Reasons...)
	s.Conflicts = append([]string(nil), s.Conflicts...)
	if s.TargetHR != nil {
		s.TargetHR = Ptr(*s.TargetHR)
	}
	if s.PaceSPerKM != nil {
		s.PaceSPerKM = Ptr(*s.PaceSPerKM)
	}
	return s
}
