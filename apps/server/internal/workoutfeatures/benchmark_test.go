package workoutfeatures

import (
	"context"
	"fmt"
	"testing"
)

func BenchmarkFoundationStages(b *testing.B) {
	for _, minutes := range []int{20, 45, 120, 240} {
		b.Run(fmt.Sprintf("%dm", minutes), func(b *testing.B) {
			raw := synthetic(minutes * 60)
			n, err := Normalize(raw)
			if err != nil {
				b.Fatal(err)
			}
			pause := BuildPause(n)
			route := BuildRoute(n, pause)
			timeline := BuildTimeline(n)
			f, err := ComputeNormalized(context.Background(), n)
			if err != nil {
				b.Fatal(err)
			}
			stages := []struct {
				name string
				run  func()
			}{{"normalization", func() {
				if _, e := Normalize(raw); e != nil {
					b.Fatal(e)
				}
			}}, {"timeline_excluding_route", func() {
				p := BuildPause(n)
				hr, speed := BuildSignal(n, p, HR), BuildSignal(n, p, Speed)
				BuildAnalysisIndex(n, p, hr, speed, route)
			}}, {"route", func() { BuildRoute(n, pause) }}, {"window_extraction", func() {
				ExtractWindows(timeline, f.Distance, f.Environment, f.Evidence.InputHash)
				ExtractSteady(n, timeline, f.Environment, &n.Parameters[0], f.Evidence.InputHash)
				ExtractLongRun(timeline)
			}}, {"artifact_serialization", func() {
				if _, _, e := f.MarshalArtifact(); e != nil {
					b.Fatal(e)
				}
			}}, {"total", func() {
				if _, e := Compute(context.Background(), raw); e != nil {
					b.Fatal(e)
				}
			}}}
			for _, stage := range stages {
				b.Run(stage.name, func(b *testing.B) {
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						stage.run()
					}
					b.StopTimer()
					data, _, _ := f.MarshalArtifact()
					b.ReportMetric(float64(len(data)), "artifact_bytes")
				})
			}
		})
	}
}
