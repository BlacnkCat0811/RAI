package athletemodel

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOwnerAuthorizationValidate(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	manifest := strings.Repeat("a", 64)
	authorization := OwnerAuthorization{
		Schema:         OwnerAuthorizationSchema,
		DatasetID:      "dataset",
		StackID:        "stack",
		ManifestSHA256: manifest,
		Scope:          OwnerAuthorizationScope,
		AuthorizedBy:   "owner",
		AuthorizedAt:   now.Add(-time.Hour).Format(time.RFC3339),
		ExpiresAt:      now.Add(time.Hour).Format(time.RFC3339),
		Statement:      OwnerAuthorizationPhrase,
	}
	if err := authorization.Validate(now, "dataset", "stack", manifest); err != nil {
		t.Fatalf("valid authorization rejected: %v", err)
	}
	cases := map[string]func(*OwnerAuthorization){
		"schema":    func(a *OwnerAuthorization) { a.Schema = "other" },
		"dataset":   func(a *OwnerAuthorization) { a.DatasetID = "other" },
		"stack":     func(a *OwnerAuthorization) { a.StackID = "other" },
		"manifest":  func(a *OwnerAuthorization) { a.ManifestSHA256 = strings.Repeat("b", 64) },
		"scope":     func(a *OwnerAuthorization) { a.Scope = "other" },
		"author":    func(a *OwnerAuthorization) { a.AuthorizedBy = " " },
		"statement": func(a *OwnerAuthorization) { a.Statement = "activate" },
		"expired":   func(a *OwnerAuthorization) { a.ExpiresAt = now.Add(-time.Minute).Format(time.RFC3339) },
		"future":    func(a *OwnerAuthorization) { a.AuthorizedAt = now.Add(time.Hour).Format(time.RFC3339) },
		"bad time":  func(a *OwnerAuthorization) { a.AuthorizedAt = "yesterday" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			invalid := authorization
			mutate(&invalid)
			if err := invalid.Validate(now, "dataset", "stack", manifest); !errors.Is(err, ErrOwnerAuthorizationInvalid) {
				t.Fatalf("expected invalid authorization, got %v", err)
			}
		})
	}
}
