import Foundation
import Testing
@testable import SyncCore

@Suite struct WorkoutBundleV4Tests {
    private let scope = Scope(serviceID: UUID().uuidString.lowercased(),
                              datasetID: UUID().uuidString.lowercased(),
                              datasetEpoch: UUID().uuidString.lowercased(),
                              collectorEpoch: UUID().uuidString.lowercased())

    private func observation(_ coverage: [String: JSON]) -> JSON {
        .object(["version": .int(1), "observedAtUs": .int(1_800_000_000_000_000),
                 "metadata": .object([:]),
                 "provenance": .object(["workout": .object(["bundleID": .string("com.apple.health.test")]),
                                         "samples": .array([]), "routes": .array([])]),
                 "coverage": .object(coverage)])
    }

    @Test func encodesColumnarV4Shape() throws {
        let source = JSON.object(["bundleID": .string("com.apple.health.test"),
                                  "name": .string("Apple Watch"), "productType": .string("Watch7,1")])
        let stream = JSON.object([
            "type": .string("HKQuantityTypeIdentifierHeartRate"),
            "kind": .string("quantity"), "unit": .string("count/min"),
            "samples": .array([.array([.int(1_800_000_000_000_000), .int(1_800_000_000_500_000), .number(120)])]),
            "uuids": .array([.string(UUID().uuidString.lowercased())]),
            "source": .int(0), "src": .array([.int(0)]),
        ])
        let raw = try WorkoutBundleV4.data(
            scope: scope, workoutID: UUID().uuidString.lowercased(),
            startUs: 1_800_000_000_000_000, endUs: 1_800_000_060_000_000,
            workout: .object(["activityType": .int(37), "durationUs": .int(60_000_000)]),
            sources: [source], streams: [stream], series: [], routes: [],
            deletions: [], routeDeletions: [],
            observation: observation(["workout": .object(["state": .string("complete"), "sampleCount": .int(1),
                                                           "queryCompleted": .bool(true), "localPersisted": .bool(true)])]))
        let json = try JSON.decode(raw)
        #expect(json["protocolVersion"].integer == 4)
        #expect(json["metadataRegistryVersion"].integer == 2)
        #expect(json["sources"].list.count == 1)
        #expect(json["streams"].list.first?["samples"].list.first?.list.count == 3)
        #expect(json["observation"]["provenance"]["samples"].list.isEmpty)
    }

    @Test func routePointIsFixedEightTupleWithNulls() throws {
        let point = RoutePoint(recordedAtUs: 1_800_000_000_000_000, latitude: 31.2, longitude: 121.4)
        let json = try WorkoutBundleV4.routePoint(point)
        #expect(json.list.count == 8)
        #expect(json.list[0].integer == 1_800_000_000_000_000)
        #expect(json.list[3] == .null)
        #expect(json.list[7] == .null)
    }

    @Test func sourceTableDeduplicatesAndOmitsDeviceDetail() throws {
        var table = WorkoutBundleV4.SourceTable()
        let first = JSON.object(["bundleID": .string("com.apple.health.test"), "name": .string("Watch"),
                                 "device": .object(["model": .string("Watch7,1")])])
        let second = JSON.object(["bundleID": .string("com.apple.health.test"), "name": .string("Watch")])
        #expect(try table.index(of: first) == 0)
        #expect(try table.index(of: second) == 0)
        #expect(table.sources.count == 1)
        #expect(table.sources[0]["device"] == .null)
    }

    @Test func pendingIndexPromotesOnlyAfterCommit() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let outbox = V4Outbox(directory: directory)
        let id = UUID().uuidString.lowercased()
        let hash = String(repeating: "a", count: 64)
        let index = V4Outbox.Index(datasetID: scope.datasetID, datasetEpoch: scope.datasetEpoch,
                                   collectorEpoch: scope.collectorEpoch,
                                   samples: ["HKQuantityTypeIdentifierHeartRate:" + UUID().uuidString.lowercased()],
                                   routes: [])
        try await outbox.savePending(index, bundleSHA256: hash)
        #expect(await outbox.load(id: id, scope: scope) == nil)
        #expect(try await outbox.promotePending(bundleSHA256: hash, id: id, scope: scope))
        #expect(await outbox.load(id: id, scope: scope) == index)
        #expect(try await outbox.promotePending(bundleSHA256: hash, id: id, scope: scope) == false)
    }

    @Test func outboxRoundTripsOnlyForMatchingScope() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let outbox = V4Outbox(directory: directory)
        let id = UUID().uuidString.lowercased()
        let index = V4Outbox.Index(datasetID: scope.datasetID, datasetEpoch: scope.datasetEpoch,
                                   collectorEpoch: scope.collectorEpoch,
                                   samples: ["HKQuantityTypeIdentifierHeartRate:" + UUID().uuidString.lowercased()],
                                   routes: [UUID().uuidString.lowercased()])
        try await outbox.record(index, id: id)
        #expect(await outbox.load(id: id, scope: scope) == index)
        let other = Scope(serviceID: scope.serviceID, datasetID: scope.datasetID,
                          datasetEpoch: UUID().uuidString.lowercased(), collectorEpoch: scope.collectorEpoch)
        #expect(await outbox.load(id: id, scope: other) == nil)
    }
}
