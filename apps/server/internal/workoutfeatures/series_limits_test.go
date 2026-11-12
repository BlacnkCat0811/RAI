package workoutfeatures

import (
	"context"
	"fmt"
	"testing"
)

// Thousands of one-point HealthKit series have the same support as one
// multi-point series. Container layout must not decide whether a run scores.
func TestFragmentedSeriesMatchesSingleSeries(t *testing.T) {
	s := synthetic(3 * 3600)
	s.Quantities = nil
	s.Routes = nil
	series := Series{Fact: FactRef{Kind: "series", ID: "all", State: SeriesBacked}, Type: HR, Source: s.Provenance[0].Epoch}
	for i := 0; i < 2433; i++ {
		at := s.Start + int64(i)*4_000_000
		series.Points = append(series.Points, Quantity{Type: HR, Start: at, End: at + 4_000_000, Value: 150, Unit: unit(HR), Source: series.Source, Support: SealedSeries})
	}
	s.Series = []Series{series}
	want, err := Compute(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	s.Series = nil
	for i, q := range series.Points {
		part := series
		part.Fact.ID = fmt.Sprint(i)
		part.Points = []Quantity{q}
		s.Series = append(s.Series, part)
	}
	got, err := Compute(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if digest(got.HeartRate) != digest(want.HeartRate) || digest(got.Qualification.Load) != digest(want.Qualification.Load) {
		t.Fatal("series fragmentation changed heart-rate support or load qualification")
	}
}

func TestSeriesContainerAndPointBudgetsRemainBounded(t *testing.T) {
	s := synthetic(60)
	s.Quantities, s.Routes = nil, nil
	s.Series = make([]Series, MaxPoints+1)
	if _, err := Normalize(s); err == nil {
		t.Fatal("unbounded empty series containers accepted")
	}
	s.Series = []Series{{Points: make([]Quantity, MaxPoints+1)}}
	if _, err := Normalize(s); err == nil {
		t.Fatal("unbounded series points accepted")
	}
}
