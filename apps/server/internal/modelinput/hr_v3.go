package modelinput

import (
	"bytes"
	"encoding/json"
	"io"
	"rai/server/internal/hrref"
	"time"
)

const SchemaHRReferenceV3 = hrref.InputSchemaV3

func validateHRV3(raw Payload, now time.Time) error {
	b, err := json.Marshal(raw)
	if err != nil {
		return ErrInvalid
	}
	var p hrref.Patch
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF || (p.MaxHR == nil && p.RestingHR == nil) {
		return ErrInvalid
	}
	if v, ok := raw["maxHR"]; ok && (v == nil || !hrref.ValidateSetting(p.MaxHR, now)) {
		return ErrInvalid
	}
	if v, ok := raw["restingHR"]; ok {
		if v == nil || p.RestingHR == nil {
			return ErrInvalid
		}
		r := p.RestingHR
		if r.Mode != "manual" && r.Mode != "automatic" {
			return ErrInvalid
		}
		obj, ok := v.(map[string]any)
		if !ok {
			return ErrInvalid
		}
		// A full resting component is explicit: null means clear, omission is rejected.
		if _, ok := obj["manualValue"]; !ok {
			return ErrInvalid
		}
		if r.Mode == "manual" && r.ManualValue == nil {
			return ErrInvalid
		}
		if r.ManualValue != nil && !hrref.ValidateSetting(r.ManualValue, now) {
			return ErrInvalid
		}
	}
	if p.MaxHR != nil && p.RestingHR != nil && p.RestingHR.ManualValue != nil && p.MaxHR.Value <= p.RestingHR.ManualValue.Value {
		return ErrInvalid
	}
	return nil
}
