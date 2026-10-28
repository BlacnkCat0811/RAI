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
		err = json.Unmarshal(v, &s.Type)
		if err == nil && s.Type != "INTERVAL" {
			s.Steps = nil
		} // a changed type cannot retain interval structure
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
	b, _ := json.Marshal(base)
	var out Base
	if e := json.Unmarshal(b, &out); e != nil {
		return Base{}, e
	}
	sessions := map[string]Session{}
	for _, s := range out.Sessions {
		sessions[s.ID] = s
	}
	for _, o := range ActiveOverrides(events) {
		if o.Status != "active" || o.Operation == "revoke" {
			continue
		}
		s, ok := sessions[o.SessionID]
		if !ok || (s.Type == "REST" && o.Anchor != nil && o.Anchor.Type != "REST") {
			if o.Anchor == nil {
				return Base{}, ErrInvalid
			}
			s = cloneSession(*o.Anchor)
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
			if e := ApplyField(&s, o.Path, o.Value); e != nil {
				return Base{}, e
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
