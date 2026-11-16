//go:build integration

package integration

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// realisticRun is a synthetic Watch-shaped outdoor run: HR every 5 s, speed,
// power and steps in native 2.56 s intervals, and a 1 Hz route with altitude
// so grade, VO2 windows, effort points and running-fitness segments are all
// exercised. Values are generated, never copied from any person.
func realisticRun(f *fixture, id string, start time.Time, minutes int, kind int) []byte {
	seconds := minutes * 60
	speedAt := func(sec float64) float64 {
		switch kind % 3 {
		case 1: // intervals after a 12-minute warm-up
			if sec > 720 && sec < float64(seconds)-600 && math.Mod(sec-720, 300) < 150 {
				return 4.6
			}
			return 3.0
		case 2: // steady tempo block
			if sec > 600 && sec < float64(seconds)-600 {
				return 3.9
			}
			return 3.1
		}
		return 3.0 + 0.15*math.Sin(sec/400)
	}
	hr := make([]float64, seconds+1)
	h := .45
	for s := 0; s <= seconds; s++ {
		target := math.Min(1, .92*speedAt(float64(s))/4.6-.02)
		tc := 30.0
		if target < h {
			tc = 50
		}
		h += (target - h) * (1 - math.Exp(-1/tc))
		hr[s] = 52 + 135*h + 0.004*float64(s)/60
	}
	var hrs, speeds, powers, steps, route []string
	for a := 0; a+5 <= seconds; a += 5 {
		hrs = append(hrs, fmt.Sprintf("[%d,%d,%.1f]", int64(a)*1e6, int64(a+5)*1e6, hr[a+2]))
	}
	const step = int64(2_560_000)
	for a := int64(0); a+step <= int64(seconds)*1e6; a += step {
		v := speedAt(float64(a+step/2) / 1e6)
		speeds = append(speeds, fmt.Sprintf("[%d,%d,%.3f]", a, a+step, v))
		powers = append(powers, fmt.Sprintf("[%d,%d,%.1f]", a, a+step, 70*v))
		steps = append(steps, fmt.Sprintf("[%d,%d,%.2f]", a, a+step, 2.56*(150+6*v)/60))
	}
	lat, lon, dist := 31.2304, 121.4737, 0.0
	for s := 0; s <= seconds; s++ {
		v := speedAt(float64(s))
		dist += v
		heading := dist / 2000
		lat += v * math.Cos(heading) / 111_000
		lon += v * math.Sin(heading) / 95_000
		alt := 12 + 6*math.Sin(dist/900)
		route = append(route, fmt.Sprintf("[%d,%.6f,%.6f,%.2f,4,3,%.2f,null]", int64(s)*1e6, lat, lon, alt, v))
	}
	stream := func(kind, unit string, samples []string) string {
		return fmt.Sprintf(`{"type":%q,"unit":%q,"status":"complete","samples":[%s]}`, kind, unit, strings.Join(samples, ","))
	}
	return []byte(fmt.Sprintf(`{
		"protocolVersion":6,
		"datasetID":%q,"datasetEpoch":%q,"collectorEpoch":%q,"workoutID":%q,
		"startUs":%d,"endUs":%d,
		"source":{"bundleID":"com.apple.health","version":"11.0","productType":"Watch7,1","operatingSystemVersion":"11.0"},
		"metadata":{"HKTimeZone":"Asia/Shanghai","HKIndoorWorkout":false},
		"workout":{"activityType":37,"durationUs":%d,"totalDistance":{"value":%.1f,"unit":"m"}},
		"streams":[%s,%s,%s,%s],
		"routes":[{"points":[%s]}]
	}`, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, id,
		start.UnixMicro(), start.Add(time.Duration(seconds)*time.Second).UnixMicro(), int64(seconds)*1_000_000, dist,
		stream(v6HRType, "count/min", hrs), stream("HKQuantityTypeIdentifierRunningSpeed", "m/s", speeds),
		stream(v6PowerType, "W", powers), stream("HKQuantityTypeIdentifierStepCount", "count", steps), strings.Join(route, ",")))
}
