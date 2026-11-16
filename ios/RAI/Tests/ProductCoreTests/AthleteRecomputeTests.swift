import Foundation
import ProductCore
import Testing

@Suite struct AthleteRecomputeTests {
  private func full() throws -> AthleteModelSnapshot {
    let file = try #require(Bundle.module.resourceURL?.appendingPathComponent("Fixtures/AthleteModel/full.json"))
    return try JSONDecoder().decode(AthleteModelSnapshot.self, from: Data(contentsOf: file))
  }

  /// The server's view of a dirty day: every slot missing with "pending".
  private func pending(_ base: AthleteModelSnapshot) -> AthleteModelSnapshot {
    var out = base
    func hide(_ e: inout MetricEnvelope) {
      e.state = ProductMetricState(rawValue: "unavailable")
      e.productReasonCodes = [ProductCode(rawValue: "model_result_pending")]
      e.primaryReason = ProductCode(rawValue: "model_result_pending")
    }
    hide(&out.trainingLoad.envelope)
    hide(&out.physiologicalRecovery.envelope)
    hide(&out.trainingReadiness.envelope)
    if out.runningFitness != nil { hide(&out.runningFitness!.envelope) }
    if out.sessionRecovery != nil { hide(&out.sessionRecovery!.envelope) }
    return out
  }

  @Test func recomputeKeepsLastReleaseOfSameStack() throws {
    let last = try full()
    #expect(!last.awaitingRecompute)
    var previous = ProductSnapshot()
    previous.athlete = last
    var loaded = ProductSnapshot()
    loaded.athlete = pending(last)
    #expect(loaded.athlete?.awaitingRecompute == true)
    let merged = loaded.keepingPublishedAthlete(from: previous)
    #expect(merged.athlete == last)
    #expect(merged.athleteUpdating)
    #expect(merged.athleteShowingPrevious)
  }

  @Test func recomputeWithoutUsablePreviousShowsPending() throws {
    let last = try full()
    var loaded = ProductSnapshot()
    loaded.athlete = pending(last)
    let first = loaded.keepingPublishedAthlete(from: ProductSnapshot())
    #expect(first.athlete == loaded.athlete)
    #expect(first.athleteUpdating)
    #expect(!first.athleteShowingPrevious)
    // Another model stack is never shown as this one's previous release.
    var other = last
    other.assignment.stackID = "rai-v2-other"
    var previous = ProductSnapshot()
    previous.athlete = other
    #expect(loaded.keepingPublishedAthlete(from: previous).athlete == loaded.athlete)
  }

  @Test func publishedReleaseClearsUpdating() throws {
    let last = try full()
    var previous = ProductSnapshot()
    previous.athlete = pending(last)
    previous.athleteUpdating = true
    var loaded = ProductSnapshot()
    loaded.athlete = last
    let merged = loaded.keepingPublishedAthlete(from: previous)
    #expect(merged.athlete == last)
    #expect(!merged.athleteUpdating)
  }

  @Test func routeCacheSkipsEmptyAndKeepsNewest() throws {
    let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
    defer { try? FileManager.default.removeItem(at: dir) }
    let cache = HomeRouteDiskCache(url: dir.appendingPathComponent("routes.json"), limit: 2)
    let point = try JSONDecoder().decode(RoutePoint.self, from: Data(#"{"latitude":1,"longitude":2,"timestampUs":3}"#.utf8))
    cache.save("a", [])
    #expect(cache.load().isEmpty)
    cache.save("a", [point]); cache.save("b", [point]); cache.save("c", [point])
    #expect(Set(cache.load().keys) == ["b", "c"])
  }

  @Test func detailCacheRejectsNonUUIDKeys() {
    let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
    defer { try? FileManager.default.removeItem(at: dir) }
    let cache = WorkoutDetailDiskCache(directory: dir)
    #expect(cache.load("../etc/passwd") == nil)
    #expect(cache.load(UUID().uuidString) == nil)
  }
}
