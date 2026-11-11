package workoutfeatures

import "math"

const StructureSchema = "rai.workout_structure/1"

type StructureSlice struct {
	Start int64   `json:"startUs"`
	End   int64   `json:"endUs"`
	Speed float64 `json:"speedMPS"`
	HR    float64 `json:"hr"`
}
type StructureFeatures struct {
	Schema string `json:"schemaVersion"`
	// Only continuous ten-second cells with <=5s native support gaps,
	// measured modest grade and usable HR can qualify as short repetitions.
	Slices []StructureSlice `json:"qualifiedTenSecondSlices"`
}

func structureFeatures(t AnalysisTimeline) *StructureFeatures {
	out := &StructureFeatures{Schema: StructureSchema, Slices: []StructureSlice{}}
	for i := 0; i+10 <= len(t.Cells); i += 10 {
		speed, hr := 0., 0.
		ok := true
		epoch := ""
		active := -1
		for j := i; j < i+10; j++ {
			c := t.Cells[j]
			if !c.continuous || c.speedGap > 5 || c.gradeTime < .9 || math.Abs(c.grade/c.gradeTime) > .03 || c.hrTime < .95 || c.hrEpoch == "ambiguous" {
				ok = false
				break
			}
			if epoch != "" && (epoch != c.epoch || active != c.activeSegment) {
				ok = false
				break
			}
			epoch = c.epoch
			active = c.activeSegment
			speed += c.speed
			hr += c.hr / c.hrTime
		}
		if ok {
			out.Slices = append(out.Slices, StructureSlice{Start: t.Start + int64(i)*1e6, End: t.Start + int64(i+10)*1e6, Speed: speed / 10, HR: hr / 10})
		}
	}
	return out
}
