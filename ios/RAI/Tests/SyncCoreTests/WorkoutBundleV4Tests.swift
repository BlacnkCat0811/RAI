import Foundation
import Testing
@testable import SyncCore

@Suite struct WorkoutBundleV4Tests {
    private let scope = Scope(serviceID: UUID().uuidString.lowercased(),
                              datasetID: UUID().uuidString.lowercased(),
                              datasetEpoch: UUID().uuidString.lowercased(),
                              collectorEpoch: UUID().uuidString.lowercased())

    @Test func encodesRawEnvelopeWithInlineSourcesAndSamples() throws {
        let source = JSON.object(["bundleID": .string("com.apple.health.test"),
                                  "name": .string("Apple Watch"), "productType": .string("Watch7,1")])
        let sample = WorkoutBundleV4.sample(uuid: UUID().uuidString.lowercased(),
                                            startUs: 1_800_000_000_000_000, endUs: 1_800_000_000_500_000,
                                            value: 120, source: source,
                                            metadata: .object(["HKIndoorWorkout": .bool(true)]))
        let stream = WorkoutBundleV4.stream(type: "HKQuantityTypeIdentifierHeartRate", unit: "count/min",
                                            status: "complete", error: nil, samples: [sample])
        let series = WorkoutBundleV4.series(type: "HKQuantityTypeIdentifierHeartRate",
                                            sampleUUID: "sample", unit: "count/min",
                                            points: [(1_800_000_000_000_000, 1_800_000_000_500_000, 121)])
        let route = WorkoutBundleV4.route(routeID: "route",
                                          source: .object(["bundleID": .string("com.apple.health.test")]),
                                          points: [.array([.int(1), .number(31.2), .number(121.4), .null, .null, .null, .null, .null])])
        let raw = try WorkoutBundleV4.data(
            scope: scope, workoutID: UUID().uuidString.lowercased(),
            startUs: 1_800_000_000_000_000, endUs: 1_800_000_060_000_000,
            source: source, metadata: .object(["HKIndoorWorkout": .bool(true)]),
            workout: .object(["activityType": .int(37), "durationUs": .int(60_000_000)]),
            streams: [stream], series: [series], routes: [route])
        let json = try JSON.decode(raw)
        #expect(json["protocolVersion"].integer == 4)
        #expect(json["datasetID"].text == scope.datasetID)
        #expect(json["datasetEpoch"].text == scope.datasetEpoch)
        #expect(json["collectorEpoch"].text == scope.collectorEpoch)
        #expect(json["source"]["bundleID"].text == "com.apple.health.test")
        #expect(json["metadata"]["HKIndoorWorkout"] == .bool(true))
        #expect(json["workout"]["activityType"].integer == 37)
        #expect(json["streams"].list.count == 1)
        #expect(json["streams"].list[0]["samples"].list.count == 1)
        #expect(json["streams"].list[0]["samples"].list[0]["source"]["bundleID"].text == "com.apple.health.test")
        #expect(json["streams"].list[0]["samples"].list[0]["metadata"]["HKIndoorWorkout"] == .bool(true))
        #expect(json["series"].list[0]["sampleUUID"].text == "sample")
        #expect(json["series"].list[0]["points"].list[0].list.count == 3)
        #expect(json["routes"].list[0]["source"]["bundleID"].text == "com.apple.health.test")
        for key in ["observation", "sources", "deletions", "routeDeletions", "metadataRegistryVersion"] {
            #expect(json.fields[key] == nil)
        }
    }

    @Test func emptyMetadataIsOmitted() throws {
        let raw = try WorkoutBundleV4.data(
            scope: scope, workoutID: UUID().uuidString.lowercased(),
            startUs: 1, endUs: 2, source: .object([:]),
            metadata: .object([:]), workout: .object([:]),
            streams: [], series: [], routes: [])
        let json = try JSON.decode(raw)
        #expect(json.fields["metadata"] == nil)
    }

    @Test func sampleWithoutMetadataOmitsTheKey() {
        let sample = WorkoutBundleV4.sample(uuid: "sample", startUs: 1, endUs: 2, value: 3,
                                            source: .object(["bundleID": .string("com.apple.health")]), metadata: nil)
        #expect(sample.fields["metadata"] == nil)
        #expect(sample.fields["value"] == .number(3))
    }

    @Test func failedStreamKeepsErrorAndEmptySamples() {
        let stream = WorkoutBundleV4.stream(type: "HKQuantityTypeIdentifierHeartRate", unit: "count/min",
                                            status: "failed", error: "healthkit_query_failed", samples: [])
        #expect(stream["status"].text == "failed")
        #expect(stream["error"].text == "healthkit_query_failed")
        #expect(stream["samples"].list.isEmpty)
    }

    @Test func routePointIsFixedEightTupleWithNulls() throws {
        let point = RoutePoint(recordedAtUs: 1_800_000_000_000_000, latitude: 31.2, longitude: 121.4)
        let json = try WorkoutBundleV4.routePoint(point)
        #expect(json.list.count == 8)
        #expect(json.list[0].integer == 1_800_000_000_000_000)
        #expect(json.list[3] == .null)
        #expect(json.list[7] == .null)
    }

    @Test func nonFiniteRouteMeasurementIsRejected() {
        let point = RoutePoint(recordedAtUs: 1, latitude: 31.2, longitude: 121.4, speed: .infinity)
        #expect(throws: SyncFailure.self) { try WorkoutBundleV4.routePoint(point) }
    }
}
