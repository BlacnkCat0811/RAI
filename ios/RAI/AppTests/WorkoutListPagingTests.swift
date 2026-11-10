import XCTest
import ObjectiveC
import HealthKit
import SyncCore
import Synchronization
@testable import RAI

/// The list walk is bounded: it keeps paging while eligible records are still
/// missing, stops at a fixed scan budget, and never re-reads a record. These
/// tests pin that rule at the App layer through the DEBUG page seam, so a
/// history whose newest records are all third-party still surfaces older
/// native runs instead of showing an empty list.
private func pagingRevision(_ bundleID: String, _ productType: String?) -> HKSourceRevision {
    let source = class_createInstance(HKSource.self, 0) as! HKSource
    source.setValue(bundleID, forKey: "bundleIdentifier")
    source.setValue("Apple Watch", forKey: "name")
    return HKSourceRevision(source: source, version: "synthetic", productType: productType,
                            operatingSystemVersion: OperatingSystemVersion(majorVersion: 26, minorVersion: 0, patchVersion: 0))
}

private func pagingWorkout(_ bundleID: String, _ productType: String?, at start: Date) -> HKWorkout {
    let workout = HKWorkout(activityType: .running, start: start, end: start.addingTimeInterval(60), duration: 60,
                            totalEnergyBurned: nil, totalDistance: nil, metadata: [HKMetadataKeyIndoorWorkout: true])
    workout.setValue(pagingRevision(bundleID, productType), forKey: "sourceRevision")
    return workout
}

/// Evaluate the actual query predicate against synthetic HealthKit objects;
/// the boundary expansion deliberately uses an unlimited query for one date.
private final class PagingHistory: Sendable {
    let workouts: [HKWorkout]
    private let evidence = Mutex((pages: 0, predicates: [String]()))
    var pages: Int { evidence.withLock { $0.pages } }
    var predicates: [String] { evidence.withLock { $0.predicates } }
    init(_ workouts: [HKWorkout]) { self.workouts = workouts }
    func page(matching predicate: NSPredicate, limit: Int) -> [HKWorkout] {
        // HealthKit's workout-type predicate uses a store key that HKWorkout
        // does not expose through KVC. Supply the same query fields explicitly
        // while evaluating the production predicate, including both dates.
        let eligible = workouts.filter { workout in
            predicate.evaluate(with: [HKPredicateKeyPathStartDate: workout.startDate,
                                      HKPredicateKeyPathEndDate: workout.endDate,
                                      HKPredicateKeyPathWorkoutType: workout.workoutActivityType.rawValue])
        }.sorted { $0.startDate > $1.startDate }
        evidence.withLock { $0.pages += 1; $0.predicates.append(predicate.predicateFormat) }
        return limit == HKObjectQueryNoLimit ? eligible : Array(eligible.prefix(limit))
    }
}

private func history(count: Int, bundleID: String, productType: String?, newest: Date) -> [HKWorkout] {
    (0..<count).map { index in
        pagingWorkout(bundleID, productType, at: newest.addingTimeInterval(-60 * Double(index)))
    }
}

final class WorkoutListPagingTests: XCTestCase {
    private let newest = Date(timeIntervalSince1970: 1_800_000_000)

    func testEligibleRecordsBehindExcludedPagesAreStillFound() async throws {
        // 100 third-party runs are newer than the only native one.
        var workouts = history(count: 100, bundleID: "com.example.thirdparty", productType: nil, newest: newest)
        let native = pagingWorkout("com.apple.health", "Watch7,1", at: newest.addingTimeInterval(-60 * 100))
        workouts.append(native)
        let source = PagingHistory(workouts)
        let client = HealthKitClient()
        await client.setWorkoutPageSource { predicate, limit in source.page(matching: predicate, limit: limit) }

        let list = try await client.recentWorkouts(limit: 100)

        XCTAssertEqual(list.filter(\.isEligible).map(\.id), [native.uuid.uuidString.lowercased()])
        XCTAssertEqual(list.count, 101)
        // Two pages: the first is full of excluded rows, the second is short
        // and ends the walk.
        let pages = source.pages
        XCTAssertEqual(pages, 2)
        let evidence = await client.workoutListEvidence()
        XCTAssertEqual(evidence["excludedWorkouts"], .int(100))
        XCTAssertEqual(evidence["eligibleWorkouts"], .int(1))
        XCTAssertEqual(evidence["scanLimit"], .int(400))
    }

    func testScanStaysBoundedWhenEverythingIsExcluded() async throws {
        let source = PagingHistory(history(count: 1_000, bundleID: "com.example.thirdparty", productType: nil, newest: newest))
        let client = HealthKitClient()
        await client.setWorkoutPageSource { predicate, limit in source.page(matching: predicate, limit: limit) }

        let list = try await client.recentWorkouts(limit: 100)

        XCTAssertTrue(list.allSatisfy(\.isExcluded))
        XCTAssertLessThanOrEqual(list.count, 400)
        let pages = source.pages
        XCTAssertLessThanOrEqual(pages, 4)
        let evidence = await client.workoutListEvidence()
        XCTAssertEqual(evidence["eligibleWorkouts"], .int(0))
        XCTAssertEqual(evidence["scanLimit"], .int(400))
    }

    func testCursorAdvancesStrictlyAndNeverRepeatsARecord() async throws {
        let source = PagingHistory(history(count: 250, bundleID: "com.example.thirdparty", productType: nil, newest: newest))
        let client = HealthKitClient()
        await client.setWorkoutPageSource { predicate, limit in source.page(matching: predicate, limit: limit) }

        let list = try await client.recentWorkouts(limit: 100)

        XCTAssertEqual(Set(list.map(\.id)).count, list.count, "a record was returned twice")
        let starts = list.map(\.start)
        XCTAssertEqual(starts, starts.sorted(by: >), "the list is not newest-first")
        let predicates = source.predicates
        XCTAssertFalse(predicates[0].contains("startDate <"))
        XCTAssertTrue(predicates.dropFirst().allSatisfy { $0.contains("startDate <") })
    }

    func testShortFirstPageEndsTheWalk() async throws {
        let source = PagingHistory(history(count: 10, bundleID: "com.apple.health", productType: "Watch7,1", newest: newest))
        let client = HealthKitClient()
        await client.setWorkoutPageSource { predicate, limit in source.page(matching: predicate, limit: limit) }

        let list = try await client.recentWorkouts(limit: 100)

        XCTAssertEqual(list.count, 10)
        XCTAssertTrue(list.allSatisfy(\.isEligible))
        let pages = source.pages
        XCTAssertEqual(pages, 1)
    }

    func testManualPageIncludesOverlappingWorkoutsByStartDate() async throws {
        let recent = pagingWorkout("com.apple.health", "Watch7,1", at: newest)
        let boundary = pagingWorkout("com.apple.health", "Watch7,1", at: newest.addingTimeInterval(-10))
        // This starts before the cursor but ends after it. An end-date filter
        // silently omitted this eligible record on the next page.
        let overlapping = pagingWorkout("com.apple.health", "Watch7,1", at: newest.addingTimeInterval(-20))
        let source = PagingHistory([recent, boundary, overlapping])
        let client = HealthKitClient()
        await client.setWorkoutPageSource { predicate, limit in source.page(matching: predicate, limit: limit) }
        let first = try await client.workoutPage(before: nil, limit: 2)
        let second = try await client.workoutPage(before: first.last?.start, limit: 2)
        XCTAssertEqual(first.count, 2)
        XCTAssertEqual(second.map(\.id), [overlapping.uuid.uuidString.lowercased()])
    }

    func testManualHistoryKeepsAllTimestampTiesAcrossPageBoundaries() async throws {
        var workouts = history(count: 99, bundleID: "com.apple.health", productType: "Watch7,1", newest: newest)
        let tiedAt = newest.addingTimeInterval(-60 * 100)
        workouts += (0..<205).map { _ in pagingWorkout("com.apple.health", "Watch7,1", at: tiedAt) }
        let oldest = pagingWorkout("com.apple.health", "Watch7,1", at: tiedAt.addingTimeInterval(-60))
        workouts.append(oldest)
        let source = PagingHistory(workouts)
        let client = HealthKitClient()
        await client.setWorkoutPageSource { predicate, limit in source.page(matching: predicate, limit: limit) }
        var cursor: Date?, all: [HealthWorkoutSummary] = [], pageSizes: [Int] = []
        for _ in 0..<4 {
            let page = try await client.workoutPage(before: cursor, limit: 100)
            all.append(contentsOf: page); pageSizes.append(page.count)
            if page.count < 100 { break }
            cursor = page.last?.start
        }
        XCTAssertEqual(pageSizes, [304, 1])
        XCTAssertEqual(Set(all.map(\.id)), Set(workouts.map { $0.uuid.uuidString.lowercased() }))
        XCTAssertEqual(all.count, workouts.count)
        let tiedIDs = all.filter { $0.start == tiedAt }.map(\.id)
        XCTAssertEqual(tiedIDs, tiedIDs.sorted())
    }
}
