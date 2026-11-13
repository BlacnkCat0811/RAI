package httpapi

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"

	"rai/server/internal/training"
)

// Mirrors required and nullable Training wire fields. Structural spelling,
// duplicate keys, primitive types and unknown fields use the shared P3 decoder.
func validateTrainingJSON(b []byte, out any) error {
	if err := trainingJSONFields(b, reflect.TypeOf(out)); err != nil {
		return err
	}
	switch r := out.(type) {
	case *training.OverrideRequest:
		if r.ExpectedOverrideRevision < 0 {
			return training.ErrInvalid
		}
	case *training.ChangeRequest:
		if r.ExpectedConfigRevision < 1 {
			return training.ErrInvalid
		}
	case *training.ExecutionChange:
		if r.ExpectedRevision < 1 {
			return training.ErrInvalid
		}
	}
	return nil
}

func trainingJSONFields(b []byte, typ reflect.Type) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == reflect.TypeOf(json.RawMessage{}) {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return training.ErrInvalid
	}
	switch typ.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(b, &fields) != nil || fields == nil {
			return training.ErrInvalid
		}
		var required []string
		switch typ {
		case reflect.TypeOf(training.CreateRequest{}):
			required = []string{"commandID", "planID", "config"}
		case reflect.TypeOf(training.ChangeRequest{}):
			required = []string{"commandID", "operation", "expectedConfigRevision"}
		case reflect.TypeOf(training.OverrideRequest{}):
			required = []string{"commandID", "expectedOverrideRevision", "overrideID", "sessionID", "operation"}
		case reflect.TypeOf(training.StartRequest{}):
			required = []string{"commandID", "executionID", "planID", "effectiveVersionID", "sessionID"}
		case reflect.TypeOf(training.ExecutionChange{}):
			required = []string{"commandID", "expectedRevision", "operation"}
		case reflect.TypeOf(training.Config{}):
			// training_config/v2 (adaptive) has an optional goal and no length.
			if v, ok := fields["version"]; ok && string(bytes.TrimSpace(v)) == `"`+training.ConfigVersionV2+`"` {
				required = []string{"version", "startDate", "availableDays", "longRunDay"}
			} else {
				required = []string{"startDate", "weeks", "goal", "availableDays", "longRunDay"}
			}
		case reflect.TypeOf(training.Goal{}):
			required = []string{"type", "date", "distanceM"}
		case reflect.TypeOf(training.ManualSession{}):
			required = []string{"sessionID", "date", "type"}
		case reflect.TypeOf(training.Step{}):
			required = []string{"kind", "durationS", "effort"}
		}
		for _, key := range required {
			if _, ok := fields[key]; !ok {
				return training.ErrInvalid
			}
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			key := strings.Split(field.Tag.Get("json"), ",")[0]
			raw, ok := fields[key]
			if !ok {
				continue
			}
			if (key == "targetHR" || key == "paceSPerKM") && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				continue
			}
			if err := trainingJSONFields(raw, field.Type); err != nil {
				return err
			}
		}
	case reflect.Slice:
		var items []json.RawMessage
		if json.Unmarshal(b, &items) != nil {
			return training.ErrInvalid
		}
		for _, item := range items {
			if err := trainingJSONFields(item, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
