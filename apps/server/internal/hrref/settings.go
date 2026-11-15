package hrref

import (
	"sort"
	"time"
)

const InputSchemaV3 = "rai.hr_reference/3"

type Setting struct {
	Value      float64    `json:"value"`
	Basis      string     `json:"basis"`
	Method     string     `json:"method"`
	MeasuredAt *time.Time `json:"measuredAt,omitempty"`
	Unit       string     `json:"unit"`
}
type RestSetting struct {
	Mode        string   `json:"mode"`
	ManualValue *Setting `json:"manualValue"`
}
type Patch struct {
	MaxHR     *Setting     `json:"maxHR,omitempty"`
	RestingHR *RestSetting `json:"restingHR,omitempty"`
}
type Event struct {
	ID, Hash, Provenance string
	From, Known          time.Time
	Through              *time.Time
	Order                int64
	Patch                Patch
	Legacy               bool
}
type ComponentEvidence struct {
	Value             *float64   `json:"value"`
	Source            string     `json:"source"`
	Basis             string     `json:"basis"`
	Status            string     `json:"status"`
	ReferenceID       string     `json:"referenceID,omitempty"`
	EffectiveFrom     *time.Time `json:"effectiveFrom,omitempty"`
	MeasuredAt        *time.Time `json:"measuredAt"`
	RecordedAt        *time.Time `json:"recordedAt,omitempty"`
	AgeDays           *int64     `json:"ageDays"`
	SampleDays        int        `json:"sampleDays"`
	LatestSampleAt    *time.Time `json:"latestSampleAt,omitempty"`
	ReviewRecommended bool       `json:"reviewRecommended"`
	Reasons           []string   `json:"reasonCodes"`
}
type Evidence struct {
	Schema      string            `json:"schemaVersion"`
	Policy      string            `json:"policyVersion"`
	StackID     string            `json:"stackID"`
	TargetAt    int64             `json:"targetAtUs"`
	EvaluatedAt int64             `json:"evaluatedAtUs"`
	InputHash   string            `json:"inputHash"`
	MaxHR       ComponentEvidence `json:"maxHR"`
	RestingHR   ComponentEvidence `json:"restingHR"`
}
type SavedSettings struct {
	Schema            string       `json:"schemaVersion"`
	MaxHR             *Setting     `json:"maxHR"`
	RestingHR         *RestSetting `json:"restingHR"`
	MaxEffectiveFrom  *time.Time   `json:"maxEffectiveFrom"`
	RestEffectiveFrom *time.Time   `json:"restEffectiveFrom"`
}

func Selected(events []Event, target, asOf time.Time) (*Event, *Event) {
	xs := append([]Event(nil), events...)
	sort.Slice(xs, func(i, j int) bool {
		a, b := xs[i], xs[j]
		if !a.From.Equal(b.From) {
			return a.From.Before(b.From)
		}
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		if !a.Known.Equal(b.Known) {
			return a.Known.Before(b.Known)
		}
		return a.ID < b.ID
	})
	var max, rest *Event
	for i := range xs {
		e := &xs[i]
		if e.From.After(target) || e.Known.After(asOf) || (e.Through != nil && !target.Before(*e.Through)) {
			continue
		}
		if e.Patch.MaxHR != nil {
			max = e
		}
		if e.Patch.RestingHR != nil {
			rest = e
		}
	}
	return max, rest
}
func Saved(events []Event, target, asOf time.Time) SavedSettings {
	m, r := Selected(events, target, asOf)
	s := SavedSettings{Schema: InputSchemaV3}
	if m != nil {
		s.MaxHR = m.Patch.MaxHR
		s.MaxEffectiveFrom = &m.From
	}
	if r != nil {
		s.RestingHR = r.Patch.RestingHR
		s.RestEffectiveFrom = &r.From
	}
	return s
}
func ValidateSetting(s *Setting, now time.Time) bool {
	if s == nil || !finite(s.Value) || s.Value < MinBPM || s.Value >= MaxBPM || s.Unit != "bpm" || (s.Basis != "measured" && s.Basis != "user_setting") {
		return false
	}
	if !methods[s.Method] && s.Method != "age_estimated" {
		return false
	}
	if s.Basis == "measured" && s.MeasuredAt == nil {
		return false
	}
	return s.MeasuredAt == nil || (s.MeasuredAt.UnixMicro() > 0 && !s.MeasuredAt.After(now))
}
func ResolveSettings(events []Event, in Input, stack string) (Snapshot, *Evidence, error) {
	target, asof := time.UnixMicro(in.WorkoutStart), time.UnixMicro(in.AsOf)
	m, r := Selected(events, target, asof)
	conflict := func(top *Event, maximum bool) bool {
		if top == nil {
			return false
		}
		for _, e := range events {
			if e.ID == top.ID || !e.From.Equal(top.From) || e.Order != top.Order || !e.Known.Equal(top.Known) || e.Known.After(asof) || (e.Through != nil && !target.Before(*e.Through)) {
				continue
			}
			if maximum && e.Patch.MaxHR != nil && top.Patch.MaxHR != nil && e.Patch.MaxHR.Value != top.Patch.MaxHR.Value {
				return true
			}
			if !maximum && e.Patch.RestingHR != nil && top.Patch.RestingHR != nil {
				a, b := e.Patch.RestingHR, top.Patch.RestingHR
				if a.Mode != b.Mode || (a.ManualValue == nil) != (b.ManualValue == nil) {
					return true
				}
				if a.ManualValue != nil && a.ManualValue.Value != b.ManualValue.Value {
					return true
				}
			}
		}
		return false
	}
	if conflict(m, true) || conflict(r, false) {
		component := ComponentEvidence{Source: "unavailable", Status: "unavailable", Reasons: []string{"reference_conflict"}}
		e := &Evidence{Schema: InputSchemaV3, Policy: PolicyV3, StackID: stack, TargetAt: in.WorkoutStart, EvaluatedAt: in.AsOf, MaxHR: component, RestingHR: component}
		return Snapshot{Schema: Schema, AsOf: in.AsOf, WorkoutAt: in.WorkoutStart, SourceEpoch: in.SourceEpoch, Reasons: []string{"reference_conflict"}, Evidence: e}, e, ErrConflict
	}

	var refs []Reference
	mode := "automatic"
	add := func(e *Event, s *Setting, max bool) {
		if e == nil || s == nil {
			return
		}
		at := e.From
		if at.After(e.Known) {
			at = e.Known
		}
		if s.MeasuredAt != nil && s.Basis == "measured" {
			at = *s.MeasuredAt
		}
		method := s.Method
		if method == "age_estimated" {
			method = "user_entered"
		}
		v := &Measurement{Value: s.Value, MeasuredAt: at.UnixMicro(), RecordedAt: e.Known.UnixMicro(), Method: method, Unit: s.Unit}
		ref := Reference{Schema: Schema, ID: e.ID, DatasetID: "settings", SubjectKey: "athlete", EffectiveFrom: e.From.UnixMicro(), KnownAt: e.Known.UnixMicro(), SourceEpoch: in.SourceEpoch, Provenance: e.Provenance, ContentHash: e.Hash}
		if e.Through != nil {
			x := e.Through.UnixMicro()
			ref.EffectiveThrough = &x
		}
		if max {
			ref.HRMax = v
		} else {
			ref.RestingHR = v
		}
		refs = append(refs, ref)
	}
	if m != nil {
		add(m, m.Patch.MaxHR, true)
	}
	if r != nil {
		mode = r.Patch.RestingHR.Mode
		add(r, r.Patch.RestingHR.ManualValue, false)
	}
	in.References = refs
	resolved, err := ResolveV3(V3Input{Input: in, Mode: mode})
	e := &Evidence{Schema: InputSchemaV3, Policy: PolicyV3, StackID: stack, TargetAt: in.WorkoutStart, EvaluatedAt: in.AsOf}
	component := func(v *Resolved, event *Event, s *Setting, source string) ComponentEvidence {
		c := ComponentEvidence{Source: source, Status: "unavailable", Reasons: []string{}}
		if v == nil {
			return c
		}
		value := v.Value
		c.Value = &value
		c.Status = "complete"
		if v.Baseline {
			c.Basis = "measured"
			c.SampleDays = v.UsableDays
			t := time.UnixMicro(v.MeasuredAt)
			c.LatestSampleAt = &t
			c.MeasuredAt = &t
		} else if event != nil && s != nil {
			c.Basis = s.Basis
			c.ReferenceID = event.ID
			c.EffectiveFrom = &event.From
			c.RecordedAt = &event.Known
			c.MeasuredAt = s.MeasuredAt
			if c.MeasuredAt == nil {
				c.Reasons = append(c.Reasons, "rhr_measurement_date_unknown")
				c.Status = "estimated"
			}
			if s.Basis == "user_setting" {
				c.Status = "estimated"
				if s.MeasuredAt == nil || s.MeasuredAt.After(target) {
					c.Reasons = append(c.Reasons, "historical_assumption")
				}
			}
			if event.Legacy {
				c.Status = "estimated"
				c.Reasons = append(c.Reasons, "legacy_date_semantics")
			}
		}
		if c.MeasuredAt != nil {
			age := int64(target.Sub(*c.MeasuredAt) / (24 * time.Hour))
			c.AgeDays = &age
		}
		return c
	}
	var ms, rs *Setting
	if m != nil {
		ms = m.Patch.MaxHR
	}
	if r != nil {
		rs = r.Patch.RestingHR.ManualValue
	}
	e.MaxHR = component(resolved.HRMax, m, ms, "manual")
	e.RestingHR = component(resolved.RestingHR, r, rs, resolved.Source)
	if e.MaxHR.Value == nil {
		e.MaxHR.Source = "unavailable"
		e.MaxHR.Reasons = append(e.MaxHR.Reasons, "hrmax_missing")
	}
	if e.RestingHR.Value == nil {
		e.RestingHR.Reasons = append(e.RestingHR.Reasons, "rhr_observations_insufficient")
	}
	if e.MaxHR.AgeDays != nil && *e.MaxHR.AgeDays > HRMaxDays {
		e.MaxHR.ReviewRecommended = true
		e.MaxHR.Reasons = append(e.MaxHR.Reasons, "hrmax_review_due")
	}
	if !resolved.RestingHRIsBaseline() && e.RestingHR.AgeDays != nil && *e.RestingHR.AgeDays > RHRDays {
		e.RestingHR.ReviewRecommended = true
		e.RestingHR.Status = "estimated"
		e.RestingHR.Reasons = append(e.RestingHR.Reasons, "rhr_manual_review_due")
	}
	if resolved.Source == "baseline_sparse" || resolved.Source == "manual_fallback" {
		e.RestingHR.Status = "estimated"
		if resolved.Source == "baseline_sparse" {
			e.RestingHR.Reasons = append(e.RestingHR.Reasons, "rhr_sparse_baseline")
		} else {
			e.RestingHR.Reasons = append(e.RestingHR.Reasons, "rhr_manual_fallback")
		}
	}
	e.MaxHR.Reasons = dedupe(e.MaxHR.Reasons)
	e.RestingHR.Reasons = dedupe(e.RestingHR.Reasons)
	// Evaluation time is audit metadata, not reference identity.
	identity := *e
	identity.EvaluatedAt = 0
	identity.InputHash = ""
	e.InputHash = Hash(struct {
		Evidence  Evidence
		Max, Rest *Event
		Lineage   []Lineage
	}{identity, m, r, func() []Lineage {
		if resolved.RestingHR != nil {
			return resolved.RestingHR.Lineage
		}
		return nil
	}()})
	resolved.Snapshot.Evidence = e
	if resolved.HRMax != nil && ms != nil {
		resolved.HRMax.Method = ms.Method
	}
	return resolved.Snapshot, e, err
}
func (v V3Result) RestingHRIsBaseline() bool { return v.RestingHR != nil && v.RestingHR.Baseline }

type SettingsResponse struct {
	Saved           SavedSettings      `json:"savedHRSettings"`
	Resolved        *Evidence          `json:"resolvedHRReference"`
	ObservedResting *RestingSuggestion `json:"observedRestingHR"`
}
