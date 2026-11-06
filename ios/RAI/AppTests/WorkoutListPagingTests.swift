import XCTest
import ObjectiveC
import HealthKit
import SyncCore
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

/// One synthetic history, newest first, served exactly the way HealthKit
/// serves it: strictly older than the cursor, at most `limit` rows.
private actor PagingHistory {
    let workouts: [HKWorkout]
    private(set) var pages = 0
    private(set) var cursors: [Date?] = []
    init(_ workouts: [HKWorkout]) { self.workouts = workouts }
    func page(before cursor: Date?, limit: Int) -> [HKWorkout] {
        pages += 1
        cursors.append(cursor)
        let eligible = workouts.filter { cursor == nil || $0.startDate < cursor! }
        return Array(eligible.prefix(limit))
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
        await client.setWorkoutPageSource { before, limit in await source.page(before: before, limit: limit) }

        let list = try await client.recentWorkouts(limit: 100)

        XCTAssertEqual(list.filter(\.isEligible).map(\.id), [native.uuid.uuidString.lowercased()])
        XCTAssertEqual(list.count, 101)
        // Two pages: the first is full of excluded rows, the second is short
        // and ends the walk.
        let pages = await source.pages
        XCTAssertEqual(pages, 2)
        let evidence = await client.workoutListEvidence()
        XCTAssertEqual(evidence["excludedWorkouts"], .int(100))
        XCTAssertEqual(evidence["eligibleWorkouts"], .int(1))
        XCTAssertEqual(evidence["scanLimit"], .int(400))
    }

    func testScanStaysBoundedWhenEverythingIsExcluded() async throws {
        let source = PagingHistory(history(count: 1_000, bundleID: "com.example.thirdparty", productType: nil, newest: newest))
        let client = HealthKitClient()
        await client.setWorkoutPageSource { before, limit in await source.page(before: before, limit: limit) }

        let list = try await client.recentWorkouts(limit: 100)

        XCTAssertTrue(list.allSatisfy(\.isExcluded))
        XCTAssertLessThanOrEqual(list.count, 400)
        let pages = await source.pages
        XCTAssertLessThanOrEqual(pages, 4)
        let evidence = await client.workoutListEvidence()
        XCTAssertEqual(evidence["eligibleWorkouts"], .int(0))
        XCTAssertEqual(evidence["scanLimit"], .int(400))
    }

    func testCursorAdvancesStrictlyAndNeverRepeatsARecord() async throws {
        let source = PagingHistory(history(count: 250, bundleID: "com.example.thirdparty", productType: nil, newest: newest))
        let client = HealthKitClient()
        await client.setWorkoutPageSource { before, limit in await source.page(before: before, limit: limit) }

        let list = try await client.recentWorkouts(limit: 100)

        XCTAssertEqual(Set(list.map(\.id)).count, list.count, "a record was returned twice")
        let starts = list.map(\.start)
        XCTAssertEqual(starts, starts.sorted(by: >), "the list is not newest-first")
        let cursors = await source.cursors
        XCTAssertNil(cursors.first ?? nil)
        XCTAssertEqual(cursors.compactMap { $0 }.count, cursors.count - 1, "a page was fetched without a cursor after the first")
    }

    func testShortFirstPageEndsTheWalk() async throws {
        let source = PagingHistory(history(count: 10, bundleID: "com.apple.health", productType: "Watch7,1", newest: newest))
        let client = HealthKitClient()
        await client.setWorkoutPageSource { before, limit in await source.page(before: before, limit: limit) }

        let list = try await client.recentWorkouts(limit: 100)

        XCTAssertEqual(list.count, 10)
        XCTAssertTrue(list.allSatisfy(\.isEligible))
        let pages = await source.pages
        XCTAssertEqual(pages, 1)
    }
}
