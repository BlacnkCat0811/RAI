package admin

import "testing"

func TestHasDownStatements(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{"-- +goose Up\nSELECT 1;", false},
		{"-- +goose Down\n-- comment\n/* multiline\ncomment */\n;", false},
		{"-- +goose Down\nDROP TABLE marker;", true},
	} {
		if got := hasDownStatements([]byte(tc.body)); got != tc.want {
			t.Errorf("hasDownStatements(%q) = %v, want %v", tc.body, got, tc.want)
		}
	}
}
