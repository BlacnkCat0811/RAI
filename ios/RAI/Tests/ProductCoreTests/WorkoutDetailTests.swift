import Foundation
import Testing
@testable import ProductCore

struct WorkoutDetailTests {
  @Test func decodesNativeDetailAndSeriesContracts() throws {
    let detailJSON = """
    {
      "workout": {
        "workoutID": "11111111-1111-1111-1111-111111111111",
        "startedAtUs": 1700000000000000, "endedAtUs": 1700003600000000,
        "durationS": 3600, "elapsedS": 3600, "distanceM": 10000, "activityType": 37,
        "activeEnergyKcal": 700, "averagePaceSPerKm": 360, "averageHeartRateBpm": 150,
        "averageCadenceSpm": 180, "averagePowerW": 250, "averageStrideLengthM": 1.1,
        "averageVerticalOscillationCm": 8.2, "averageGroundContactTimeMs": 240,
        "indoor": false, "sessionType": "TEMPO", "sourceCategory": "apple_watch",
        "analysisStatus": "published", "analysisAvailable": true,
        "modelVersion": "workout.features/v2+training_load.trimp/v1",
        "assessmentStatus": "published", "qualityScore": 0.82,
        "heartRateCoverage": 0.95, "heartRateSampleCount": 120, "routeQuality": 0.9,
        "routePointCount": 500, "sourceConfidence": 1, "stimulusConfidence": 0.7,
        "stimulusType": "lactate_threshold", "missingReasons": [], "explanationCodes": ["detail_series_sampled_600"]
      },
      "environment": {
        "weatherCondition": null, "temperatureC": 30, "humidityPercent": 80,
        "status": "applied", "policyVersion": "environment.correction/v1",
        "reasonCodes": ["heat_humidity_adjustment"],
        "correction": {"environment": 0.0375, "elevation": 0.0, "total": 0.0375},
        "adjusted": {"durationS": 3469.9, "paceSPerKm": 347.0}
      },
      "summary": {"averagePaceSPerKm": 360, "gradeAdjustedPaceSPerKm": 350, "coveredSplits": 10, "totalSplits": 10, "elevationGainM": 50, "routeQuality": 0.9},
      "trainingLoad": {"load": 120, "intensity": 2, "confidence": 1, "source": "training_load.trimp/v1", "stimulusType": "unclassified", "rpe": null, "missingReasons": []},
      "zoneSummary": {
        "zoneProfileID": "hrr:50:200", "stimulusType": "lactate_threshold", "secondaryStimulusTypes": ["aerobic_base"],
        "stimulusConfidence": 0.7, "heartRateCoverage": 0.95, "paceCoverage": 0,
        "heartRateExposure": [{"zone": "lactate_threshold", "durationSeconds": 600, "distanceMeters": 0, "fraction": 0.2}],
        "paceExposure": [], "stimulusScores": [{"stimulusType": "lactate_threshold", "qualifyingSeconds": 600, "targetSeconds": 1200, "score": 0.35, "confidence": 0.7}],
        "explanationCodes": ["hr_zones_from_parameter_set"], "paceZoneStatus": "threshold_pace_unavailable"
      },
      "route": {"points": [{"latitude": 31.2, "longitude": 121.4, "elevationM": 10, "speedMPS": 3, "timestampUs": 1700000000000000}], "kilometerMarkers": [], "pointCount": 1},
      "override": null
    }
    """
    let detail = try JSONDecoder().decode(WorkoutDetailResponse.self, from: Data(detailJSON.utf8))
    #expect(detail.workout.workoutID.hasPrefix("1111"))
    #expect(detail.workout.averagePaceSPerKm == 360)
    #expect(detail.environment?.status == "applied")
    #expect(detail.zoneSummary?.stimulusScores.first?.score == 0.35)
    #expect(detail.trainingLoad?.load == 120)
    #expect(detail.route.points.count == 1)

    let seriesJSON = """
    {
      "workoutID": "11111111-1111-1111-1111-111111111111",
      "startedAtUs": 1700000000000000, "endedAtUs": 1700003600000000, "durationS": 3600,
      "splits": [{"index": 1, "distanceM": 1000, "durationS": 360, "averagePaceSPerKm": 360, "averageHeartRateBpm": 150, "averageCadenceSpm": 180,
        "grade": {"policyVersion": "grade_adjustment.minetti/v1", "elevationChangeMeters": 10, "gainMeters": 10, "lossMeters": 0, "gradePercent": 1, "gradeAdjustedPaceSPerKm": 356, "reasonCodes": []}}],
      "grade": {"policyVersion": "grade_adjustment.minetti/v1", "coveredSplits": 1, "totalSplits": 1, "gainMeters": 10, "lossMeters": 0, "gradeAdjustedPaceSPerKm": 356, "reasonCodes": []},
      "segments": [], "segmentPlan": null, "segmentNote": "", "hasMeasuredWorkoutSteps": false, "dynamics": null,
      "series": {
        "pace": {"unit": "s/km", "totalSamples": 1, "points": [{"offsetS": 0, "value": 360}]},
        "heartRate": {"unit": "count/min", "totalSamples": 1, "points": [{"offsetS": 0, "value": 150}]},
        "cadence": {"unit": "spm", "totalSamples": 0, "points": []},
        "strideLength": {"unit": "m", "totalSamples": 0, "points": []},
        "power": {"unit": "W", "totalSamples": 0, "points": []},
        "groundContactTime": {"unit": "ms", "totalSamples": 0, "points": []},
        "verticalOscillation": {"unit": "cm", "totalSamples": 0, "points": []},
        "elevation": {"unit": "m", "totalSamples": 1, "points": [{"offsetS": 0, "value": 10}]}
      }
    }
    """
    let series = try JSONDecoder().decode(WorkoutSeriesResponse.self, from: Data(seriesJSON.utf8))
    #expect(series.splits.count == 1)
    #expect(series.series.pace.points.first?.value == 360)
    #expect(WorkoutMetricKind.pace.series(series.series).totalSamples == 1)
  }

  @Test func decodesFreshnessStatesAndEmptyBundle() throws {
    let json = """
    {"workoutID":"11111111-1111-1111-1111-111111111111","facts":{"committedAt":"2026-10-31T00:00:00Z","lastBatchAt":"","batches":1},
     "analysis":{"status":"pending","available":false,"jobID":"","publishedAt":""},
     "series":[{"metric":"pace","samples":0,"state":"not_seen_yet","lastAvailableAt":""}],
     "completeness":"partial","deliveryState":"delivered","analysisState":"pending","stabilityState":"settling"}
    """
    let freshness = try JSONDecoder().decode(WorkoutFreshness.self, from: Data(json.utf8))
    #expect(freshness.seriesState("pace") == "not_seen_yet")
    #expect(WorkoutSeriesBundle.empty.pace.points.isEmpty)
  }
  @Test func modelQualificationExplainsWhyARunIsNotUsed() throws {
    let json = #"{"runningAbility":{"status":"ineligible","reasonCodes":["effort_unconfirmed","grade_unknown","new_future_reason"]},"trainingLoad":{"status":"conditional","reasonCodes":[]}}"#
    let q = try JSONDecoder().decode(WorkoutModelQualification.self, from: Data(json.utf8))
    #expect(WorkoutQualificationText.runningAbility(q.runningAbility) == "跑力证据：未采用（未确认为全力、坡度未知、new_future_reason）")
    #expect(WorkoutQualificationText.trainingLoad(q.trainingLoad) == "负荷：估算")
    #expect(WorkoutQualificationText.trainingLoad(WorkoutQualification(status: "pending", reasonCodes: [])) == "负荷：计算中")
  }
}
