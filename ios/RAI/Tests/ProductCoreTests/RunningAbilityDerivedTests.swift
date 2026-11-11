import Foundation
import Testing
@testable import ProductCore

@Suite struct RunningAbilityDerivedTests {
  @Test func paceAtV60() {
    let pace = RunningAbilityMath.paceSecondsPerKm(v60: 4)
    #expect(pace != nil)
    #expect(abs((pace ?? 0) - 250) < 0.0001)
    #expect(RunningAbilityMath.paceSecondsPerKm(v60: nil) == nil)
    #expect(RunningAbilityMath.paceSecondsPerKm(v60: 0) == nil)
    #expect(RunningAbilityMath.paceSecondsPerKm(v60: .nan) == nil)
  }

  @Test func paceZonesAreOrderedAndComplete() {
    let zones = RunningAbilityMath.paceZones(v60: 4)
    #expect(zones.map(\.code) == ["E", "M", "T", "I", "R"])
    for zone in zones {
      #expect(zone.slowSecondsPerKm > zone.fastSecondsPerKm)
      #expect(zone.fastSecondsPerKm > 0)
    }
    // Faster bands must have faster paces than the easy band.
    #expect(zones[4].fastSecondsPerKm < zones[0].fastSecondsPerKm)
    // Easy band at 4 m/s: slow end 1000 / (4 * 0.68) = 367.6 s/km,
    // fast end 1000 / (4 * 0.80) = 312.5 s/km.
    #expect(abs(zones[0].slowSecondsPerKm - 367.6471) < 0.01)
    #expect(abs(zones[0].fastSecondsPerKm - 312.5) < 0.01)
    // Threshold brackets the 60-minute pace itself (250 s/km at 4 m/s).
    #expect(zones[2].slowSecondsPerKm > 250 && zones[2].fastSecondsPerKm < 250)
  }

  @Test func racePredictionsGrowWithDistance() {
    let predictions = RunningAbilityMath.racePredictions(v60: 4, gamma: 1 / 1.06)
    #expect(predictions.map(\.name) == ["5 公里", "10 公里", "半程马拉松", "马拉松"])
    #expect(predictions[0].seconds < predictions[1].seconds)
    #expect(predictions[1].seconds < predictions[2].seconds)
    #expect(predictions[2].seconds < predictions[3].seconds)
    // A 4 m/s runner covers 14.4 km in 60 minutes; the 5 km projection is
    // therefore comfortably under 20 minutes.
    #expect(predictions[0].seconds > 900)
    #expect(predictions[0].seconds < 1_200)
  }

  @Test func predictionsUseFittedGamma() {
    // γ = 1 / 1.06 reproduces Riegel; a lower γ (less endurance) is slower
    // beyond the hour and faster below it.
    let riegel = RunningAbilityMath.racePredictions(v60: 4, gamma: 1 / 1.06)
    let fitted = RunningAbilityMath.racePredictions(v60: 4, gamma: 1 / 1.06)
    #expect(abs(riegel[3].seconds - fitted[3].seconds) < 0.001)
    let weak = RunningAbilityMath.racePredictions(v60: 4, gamma: 0.89)
    #expect(weak[3].seconds > riegel[3].seconds)
    #expect(weak[0].seconds < riegel[0].seconds)
    #expect(RunningAbilityMath.projectionGamma(1.2) == 0.97)
    #expect(RunningAbilityMath.projectionGamma(0.5) == 0.88)
  }

  @Test func invalidInputProducesNoPredictions() {
    #expect(RunningAbilityMath.racePredictions(v60: 4).isEmpty)
    #expect(RunningAbilityMath.paceZones(v60: nil).isEmpty)
    #expect(RunningAbilityMath.paceZones(v60: 0).isEmpty)
    #expect(RunningAbilityMath.paceZones(v60: .infinity).isEmpty)
    #expect(RunningAbilityMath.racePredictions(v60: -1).isEmpty)
    #expect(RunningAbilityMath.racePredictions(v60: .nan).isEmpty)
  }

  @Test func retentionNormalizesBothPayloadForms() {
    let fraction = RunningAbilityMath.retentionPercent(0.82)
    let percent = RunningAbilityMath.retentionPercent(82)
    #expect(fraction != nil && abs((fraction ?? 0) - 82) < 0.0001)
    #expect(percent != nil && abs((percent ?? 0) - 82) < 0.0001)
    #expect(RunningAbilityMath.retentionPercent(nil) == nil)
    #expect(RunningAbilityMath.retentionPercent(-0.1) == nil)
  }
}
