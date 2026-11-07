package hrref

import "sort"

// SourceDescriptor is the equivalence class used to form a source epoch. Any
// source, product, bundle or descriptor change starts a new epoch; it cannot
// inherit trend or confidence from the old one.
type SourceDescriptor struct {
	Source         string `json:"source"`
	ProductType    string `json:"productType"`
	BundleID       string `json:"bundleID"`
	Version        string `json:"version"`
	DescriptorHash string `json:"descriptorHash"`
}

func Epoch(d SourceDescriptor) string { return Hash(d) }

func SameEpoch(a, b string) bool { return a != "" && a == b }

// PoolSupportCap caps cross-epoch support at 0.4 until an independent
// calibration exists. It never asserts that the epochs are comparable.
func PoolSupportCap(sameEpoch, calibrated bool) float64 {
	if sameEpoch || calibrated {
		return 1
	}
	return .4
}

type SourceSample struct {
	Start, End     int64
	SpeedMPS       *float64
	Steps          *float64
	Cadence        *float64
	StepsKnown     bool
	RouteSpeedMPS  *float64
	SourceConflict bool
}

// SuspectedMislabel implements the conservative false-labelling rule: at least
// 120 continuous seconds at >=6 m/s and >=90% of that interval covered by
// explicit step evidence with cadence <80 spm. Missing step data is unknown,
// never zero, so it cannot trigger the rule.
func SuspectedMislabel(segs []SourceSample) bool {
	sort.Slice(segs, func(i, j int) bool { return segs[i].Start < segs[j].Start })
	type block struct{ s, e int64 }
	blocks := []block{}
	var cur *block
	for _, x := range segs {
		if x.SpeedMPS == nil || *x.SpeedMPS < 6 || x.End <= x.Start {
			if cur != nil {
				blocks = append(blocks, *cur)
				cur = nil
			}
			continue
		}
		if cur != nil && x.Start <= cur.e+1_000_000 {
			if x.End > cur.e {
				cur.e = x.End
			}
		} else {
			if cur != nil {
				blocks = append(blocks, *cur)
			}
			cur = &block{x.Start, x.End}
		}
	}
	if cur != nil {
		blocks = append(blocks, *cur)
	}
	for _, b := range blocks {
		if b.e-b.s < 120_000_000 {
			continue
		}
		covered, low := int64(0), int64(0)
		for _, x := range segs {
			s, e := max64(b.s, x.Start), min64(b.e, x.End)
			if e <= s || !x.StepsKnown {
				continue
			}
			cad := x.Cadence
			if cad == nil && x.Steps != nil && e > s {
				v := *x.Steps / (float64(e-s) / 60_000_000_000)
				cad = &v
			}
			if cad == nil {
				continue
			}
			covered += e - s
			if *cad < 80 {
				low += e - s
			}
		}
		if covered*100 >= (b.e-b.s)*90 && low*100 >= covered*90 {
			return true
		}
	}
	return false
}

// QualityFlags records route/speed disagreement and source conflicts without
// reclassifying the workout's sport.
func QualityFlags(segs []SourceSample) []string {
	out := []string{}
	for _, x := range segs {
		if x.SourceConflict {
			out = append(out, "source_conflict")
		}
		if x.SpeedMPS != nil && x.RouteSpeedMPS != nil && *x.RouteSpeedMPS > 0 {
			diff := abs(*x.SpeedMPS-*x.RouteSpeedMPS) / *x.RouteSpeedMPS
			if diff > .05 {
				out = append(out, "route_speed_mismatch")
			}
		}
	}
	return dedupe(out)
}
func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
