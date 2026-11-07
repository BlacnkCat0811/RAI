import Foundation
import Testing
@testable import SyncCore

@Suite struct HealthIntegrityTests {
    @Test func provenanceUsesEvidenceAndNeverNames() throws {
        let native: JSON = .object(["bundleID": .string("com.apple.health"), "version": .string("1"), "productType": .string("Watch7,1")])
        #expect(HealthSourcePolicy.classification(native) == "apple_watch")
        #expect(HealthSourcePolicy.classification(.object(["bundleID": .string("com.apple.health"), "name": .string("Apple Watch")])) == "source_unknown")
        #expect(HealthSourcePolicy.classification(.object(["bundleID": .string("third.party"), "productType": .string("Watch7,1")])) == "third_party")
        let child: JSON = .object(["bundleID": .string("com.apple.health"), "version": .string("1")])
        #expect(HealthSourcePolicy.classification(child, associatedWorkout: native) == "associated_apple_watch")
        #expect(child["device"] == .null)
        #expect(HealthSourcePolicy.classification(.object([:])) == "source_unknown")
        #expect(HealthSourcePolicy.classification(.object(["bundleID": .string("com.apple.health"), "productType": .string("iPhone13,4")])) == "source_unknown")
    }
    @Test func coverageEmptyFailedUnknownAndProcessingAreDistinct() throws {
        let empty = WorkoutCoverage.result(count: 0)
        #expect(empty.state == .noRecordsObserved); #expect(!empty.isAvailable)
        #expect(empty.queryCompleted == true); #expect(empty.localPersisted == false); #expect(empty.serverCommitted == nil)
        #expect(WorkoutCoverage(state: .notQueried).wire["queryCompleted"] == .bool(false))
        #expect(WorkoutCoverage.result(count: 3, completed: false).state == .partial)
        #expect(WorkoutCoverage.result(count: 3).state == .complete)
        let legacy = try JSONDecoder().decode(WorkoutCoverage.self, from: Data(#"{"state":"available","sampleCount":3}"#.utf8))
        #expect(legacy.state == .available); #expect(legacy.serverCommitted == nil)
    }
    @Test func routeCaptureReplaysPrefixKeepsAllTimestampsAndInterruptedBytes() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("rai-route-capture-" + Wire.id())
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let url = dir.appendingPathComponent("points.ndjson")
        let timestamps: [Int64] = [10,20,15,15,12,30]
        let points = timestamps.map { RoutePoint(recordedAtUs: $0, latitude: 0, longitude: 0) }
        do { let capture = try RouteCaptureFile(url: url); for point in points.prefix(3) { try capture.append(point) } }
        let original = try Data(contentsOf: url)
        #expect(original.split(separator: 10).count == 3)
        do {
            let capture = try RouteCaptureFile(url: url)
            for batch in [Array(points.prefix(2)), Array(points.dropFirst(2))] { for point in batch { try capture.append(point) } }
            try capture.finish(expected: 6)
        }
        let lines = try Data(contentsOf: url).split(separator: 10)
        #expect(lines.count == 6)
        for (i,line) in lines.enumerated() { let point = try JSON.decode(Data(line)); #expect(point["sequence"].integer == Int64(i)); #expect(point["recordedAtUs"].integer == timestamps[i]) }
        #expect(try Data(contentsOf: url).starts(with: original))
        let before = try Data(contentsOf: url)
        let changed = try RouteCaptureFile(url: url)
        #expect(throws: (any Error).self) { try changed.append(RoutePoint(recordedAtUs: 11, latitude: 0, longitude: 0)) }
        #expect(try Data(contentsOf: url) == before)
    }
    @Test func routeIncompleteTailAndCountNeverComplete() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("rai-route-incomplete-" + Wire.id())
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let url = dir.appendingPathComponent("points.ndjson"), point = RoutePoint(recordedAtUs: 0, latitude: 0, longitude: 0)
        let capture = try RouteCaptureFile(url: url); try capture.append(point)
        #expect(throws: (any Error).self) { try capture.finish(expected: 2) }
        let corrupt = dir.appendingPathComponent("corrupt.ndjson"); try Data("{partial".utf8).write(to: corrupt)
        let reopened = try RouteCaptureFile(url: corrupt)
        #expect(throws: (any Error).self) { try reopened.append(point) }
        #expect(try Data(contentsOf: corrupt) == Data("{partial".utf8))
    }
    @Test func routeSpoolChunkBoundaryRegressionAndIllegalValues() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("rai-route-clock-" + Wire.id())
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let spool = try RouteSpool(directory: dir, chunkSize: 2)
        let stamps: [Int64] = [10,20,5,5,30]
        try spool.append(stamps.map { RoutePoint(recordedAtUs: $0, latitude: 0, longitude: 0) })
        let route = try spool.finish()
        #expect(route.chunks.count == 3); #expect(route.pointCount == 5)
        #expect(route.points.map(\.recordedAtUs) == stamps); #expect(route.first == 5); #expect(route.last == 30)
        #expect(throws: (any Error).self) { try RoutePoint(recordedAtUs: Int64.min, latitude: 0, longitude: 0).canonical(sequence: 0) }
        #expect(throws: (any Error).self) { try RoutePoint(recordedAtUs: 0, latitude: .nan, longitude: 0).canonical(sequence: 0) }
    }
}

extension HealthIntegrityTests {
    @Test func deviceScopedNativeWatchRequiresProductAndDevice() {
        let native: JSON = .object(["bundleID": .string("com.apple.health." + Wire.id()), "version": .string("1"), "productType": .string("Watch8,3"), "device": .object(["manufacturer": .string("Apple Inc."), "model": .string("Watch")])])
        #expect(HealthSourcePolicy.classification(native) == "apple_watch")
        var child = native.fields; child.removeValue(forKey: "device")
        #expect(HealthSourcePolicy.classification(.object(child)) == "source_unknown")
        #expect(HealthSourcePolicy.classification(.object(child), associatedWorkout: native) == "associated_apple_watch")
        child["productType"] = .string("iPhone13,4")
        #expect(HealthSourcePolicy.classification(.object(child), associatedWorkout: native) == "associated_apple_watch")
        var unrelated = child; unrelated["version"] = .string("2")
        #expect(HealthSourcePolicy.classification(.object(unrelated), associatedWorkout: native) == "source_unknown")
    }
}
