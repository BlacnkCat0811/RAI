package postgres

import (
	"testing"
	"time"
)

func TestTrainingWindowCalendarBoundaries(t *testing.T) {
	for _, tc := range []struct{ zone, day, start, end string }{
		{"Asia/Shanghai", "2026-10-29", "2026-10-01T16:00:00Z", "2026-10-29T16:00:00Z"},
		{"America/New_York", "2026-03-08", "2026-02-09T05:00:00Z", "2026-03-09T04:00:00Z"},
		{"America/New_York", "2026-11-01", "2026-10-05T04:00:00Z", "2026-11-02T05:00:00Z"},
		{"Pacific/Apia", "2011-12-31", "2011-12-04T10:00:00Z", "2011-12-31T10:00:00Z"},
	} {
		t.Run(tc.zone+tc.day, func(t *testing.T) {
			a, b, e := TrainingWindow(tc.zone, tc.day)
			if e != nil || a.Format(time.RFC3339) != tc.start || b.Format(time.RFC3339) != tc.end {
				t.Fatal("incorrect instant boundaries", e, a, b)
			}
		})
	}
	if _, _, e := TrainingWindow("UTC", "2026-02-30"); e == nil {
		t.Fatal("invalid date accepted")
	}
}
