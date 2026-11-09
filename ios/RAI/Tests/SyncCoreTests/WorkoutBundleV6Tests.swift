import Foundation
import Testing
@testable import SyncCore

private func numeric(_ value: JSON) -> Double? {
    switch value {
    case .int(let integer): return Double(integer)
    case .number(let number): return number
    default: return nil
    }
}

@Suite struct WorkoutBundleV6Tests {
    private let scope = Scope(serviceID: UUID().uuidString.lowercased(),
                              datasetID: UUID().uuidString.lowercased(),
                              datasetEpoch: UUID().uuidString.lowercased(),
                              collectorEpoch: UUID().uuidString.lowercased())
    private let startUs: Int64 = 1_800_000_000_000_000
    private let workoutID = UUID().uuidString.lowercased()

    private var source: JSON {
        .object(["bundleID": .string("com.apple.health"), "name": .string("Apple Watch"),
                 "productType": .string("Watch7,1")])
    }

    private func encode(streams: [WorkoutStreamV6], routes: [[RoutePoint]] = [],
                        metadata: JSON? = nil, endUs: Int64? = nil) throws -> (Data, JSON) {
        let raw = try WorkoutBundleV6.data(
            scope: scope, workoutID: workoutID, startUs: startUs,
            endUs: endUs ?? startUs + 2_400_000_000,
            source: source, metadata: metadata,
            workout: .object(["activityType": .int(37), "durationUs": .int(2_400_000_000)]),
            streams: streams, routes: routes)
        return (raw, try JSON.decode(raw))
    }

    @Test func encodesProtocolSixWithoutPerSampleIdentity() throws {
        let hr = WorkoutStreamV6(
            type: "HKQuantityTypeIdentifierHeartRate", unit: "count/min", status: "complete", error: nil,
            samples: [WorkoutSampleV6(startUs: startUs + 1_000_000, endUs: startUs + 1_500_000, value: 120)],
            series: [])
        let power = WorkoutStreamV6(
            type: "HKQuantityTypeIdentifierRunningPower", unit: "W", status: "complete", error: nil,
            samples: [],
            series: [WorkoutSeriesGroupV6(
                startUs: startUs + 2_000_000, endUs: startUs + 3_000_000, value: 250,
                points: [WorkoutSampleV6(startUs: startUs + 2_100_000, endUs: startUs + 2_500_000, value: 260),
                         WorkoutSampleV6(startUs: startUs + 2_600_000, endUs: startUs + 2_900_000, value: 240)])])
        let route = [RoutePoint(recordedAtUs: startUs + 1_000_000, latitude: 31.2, longitude: 121.4,
                                altitude: 10, speed: 3)]
        let (_, json) = try encode(streams: [power, hr], routes: [route])

        #expect(json["protocolVersion"].integer == 6)
        #expect(json["datasetID"].text == scope.datasetID)
        #expect(json["datasetEpoch"].text == scope.datasetEpoch)
        #expect(json["collectorEpoch"].text == scope.collectorEpoch)
        #expect(json["workoutID"].text == workoutID)
        #expect(json["startUs"].integer == startUs)
        #expect(json["source"]["bundleID"].text == "com.apple.health")
        #expect(json["workout"]["activityType"].integer == 37)
        #expect(json.fields["metadata"] == nil)

        let streams = json["streams"].list
        #expect(streams.map { $0["type"].text } == ["HKQuantityTypeIdentifierHeartRate",
                                                    "HKQuantityTypeIdentifierRunningPower"])
        let encoded = streams[0]
        #expect(encoded["unit"].text == "count/min")
        #expect(encoded["status"].text == "complete")
        let sample = encoded["samples"].list[0]
        #expect(sample.list.count == 3)
        #expect(sample.list[0].integer == 1_000_000)
        #expect(sample.list[1].integer == 1_500_000)
        #expect(numeric(sample.list[2]) == 120)
        for key in ["uuid", "metadata", "source"] {
            #expect(sample.fields[key] == nil)
        }

        let group = streams[1]["series"].list[0]
        #expect(group["startDeltaUs"].integer == 2_000_000)
        #expect(group["endDeltaUs"].integer == 3_000_000)
        #expect(numeric(group["value"]) == 250)
        #expect(group["points"].list.count == 2)
        #expect(group["points"].list[0].list.count == 3)

        let routeJSON = json["routes"].list[0]
        #expect(routeJSON.fields.keys.sorted() == ["points"])
        // [delta, latitude, longitude, altitude, null, null, speed]
        #expect(routeJSON["points"].list[0].list.count == 7)
        #expect(routeJSON["points"].list[0].list[4] == .null)

        // Identity and source are server-owned; no legacy lanes may appear.
        for key in ["series", "sources", "sourceTable", "deletions", "routeDeletions",
                    "observation", "metadataRegistryVersion"] {
            #expect(json.fields[key] == nil)
        }
    }

    @Test func metadataIsBothOptionalAndPreserved() throws {
        let empty = try encode(streams: [], metadata: .object([:])).1
        #expect(empty.fields["metadata"] == nil)
        let kept = try encode(streams: [], metadata: .object(["HKIndoorWorkout": .bool(true)])).1
        #expect(kept["metadata"]["HKIndoorWorkout"] == .bool(true))
    }

    @Test func orderingIsStableAcrossInputOrder() throws {
        let early = WorkoutSampleV6(startUs: startUs + 1, endUs: startUs + 2, value: 1)
        let middle = WorkoutSampleV6(startUs: startUs + 3, endUs: startUs + 4, value: 2)
        let late = WorkoutSampleV6(startUs: startUs + 5, endUs: startUs + 6, value: 3)
        let groupEarly = WorkoutSeriesGroupV6(startUs: startUs, endUs: startUs + 3,
                                              value: 1, points: [early])
        let groupLate = WorkoutSeriesGroupV6(startUs: startUs + 3, endUs: startUs + 5,
                                             value: 2, points: [middle])
        let a = WorkoutStreamV6(type: "HKQuantityTypeIdentifierRunningPower", unit: "W",
                                status: "complete", error: nil, samples: [late, early, middle],
                                series: [groupLate, groupEarly])
        let b = WorkoutStreamV6(type: "HKQuantityTypeIdentifierHeartRate", unit: "count/min",
                                status: "complete", error: nil, samples: [middle],
                                series: [])
        let first = try encode(streams: [a, b])
        let second = try encode(streams: [WorkoutStreamV6(
            type: a.type, unit: a.unit, status: a.status, error: a.error,
            samples: [early, middle, late], series: [groupEarly, groupLate]), b])
        #expect(first.0 == second.0)
        let streams = first.1["streams"].list
        #expect(streams.map { $0["type"].text } == ["HKQuantityTypeIdentifierHeartRate",
                                                    "HKQuantityTypeIdentifierRunningPower"])
        #expect(streams[1]["samples"].list.map { $0.list[0].integer } == [1, 3, 5])
        #expect(streams[1]["series"].list.map { $0["startDeltaUs"].integer } == [0, 3])
    }

    @Test func seriesGroupKeepsParentHeaderAndRejectsInvalidPoints() throws {
        let group = WorkoutSeriesGroupV6(
            startUs: startUs + 10, endUs: startUs + 20, value: 42,
            points: [WorkoutSampleV6(startUs: startUs + 12, endUs: startUs + 15, value: 40)])
        let json = try WorkoutBundleV6.seriesGroup(group, base: startUs)
        #expect(json["startDeltaUs"].integer == 10)
        #expect(json["endDeltaUs"].integer == 20)
        #expect(numeric(json["value"]) == 42)
        #expect(json["points"].list[0].list[0].integer == 12)

        #expect(throws: SyncFailure.self) {
            try WorkoutBundleV6.seriesGroup(
                WorkoutSeriesGroupV6(startUs: 1, endUs: 2, value: 3, points: []), base: 0)
        }
        #expect(throws: SyncFailure.self) {
            try WorkoutBundleV6.seriesGroup(
                WorkoutSeriesGroupV6(startUs: startUs + 10, endUs: startUs + 20, value: 42,
                                     points: [WorkoutSampleV6(startUs: startUs + 5, endUs: startUs + 15,
                                                              value: 40)]),
                base: startUs)
        }
    }

    @Test func routePointOmitsTrailingUnavailableMeasurements() throws {
        let plain = try WorkoutBundleV6.routePoint(
            RoutePoint(recordedAtUs: startUs + 5, latitude: 31.2, longitude: 121.4), base: startUs)
        #expect(plain.list.count == 3)
        #expect(plain.list[0].integer == 5)
        let internalNull = try WorkoutBundleV6.routePoint(
            RoutePoint(recordedAtUs: startUs + 5, latitude: 31.2, longitude: 121.4, speed: 3), base: startUs)
        #expect(internalNull.list.count == 7)
        #expect(internalNull.list[3] == .null)
        #expect(internalNull.list[4] == .null)
        #expect(internalNull.list[5] == .null)
        #expect(numeric(internalNull.list[6]) == 3)
        #expect(throws: SyncFailure.self) {
            try WorkoutBundleV6.routePoint(
                RoutePoint(recordedAtUs: 1, latitude: .infinity, longitude: 0), base: 0)
        }
    }

    @Test func routePointsAreSortedDeterministically() throws {
        let early = RoutePoint(recordedAtUs: startUs + 2, latitude: 31.2002, longitude: 121.4002)
        let late = RoutePoint(recordedAtUs: startUs + 9, latitude: 31.2001, longitude: 121.4001)
        let sameTimeFirst = RoutePoint(recordedAtUs: startUs + 5, latitude: 31.1, longitude: 121.1)
        let sameTimeSecond = RoutePoint(recordedAtUs: startUs + 5, latitude: 31.2, longitude: 121.2)
        let (_, json) = try encode(streams: [], routes: [[late, sameTimeSecond, early, sameTimeFirst]])
        let points = json["routes"].list[0]["points"].list
        #expect(points.map { $0.list[0].integer } == [2, 5, 5, 9])
        #expect(numeric(points[1].list[1]) == 31.1)
        #expect(numeric(points[2].list[1]) == 31.2)
    }

    @Test func overLimitEntriesAndRoutesThrowWithoutTruncation() throws {
        let samples = Array(repeating: WorkoutSampleV6(startUs: startUs, endUs: startUs + 1, value: 1),
                            count: WorkoutBundleV6.maxSamples + 1)
        #expect(throws: SyncFailure.self) {
            try encode(streams: [WorkoutStreamV6(type: "HKQuantityTypeIdentifierHeartRate",
                                                 unit: "count/min", status: "complete", error: nil,
                                                 samples: samples, series: [])])
        }
        let routes = Array(repeating: [RoutePoint(recordedAtUs: startUs, latitude: 31.2, longitude: 121.4)],
                           count: WorkoutBundleV6.maxRoutes + 1)
        #expect(throws: SyncFailure.self) { try encode(streams: [], routes: routes) }
    }

    @Test func fortyMinuteSyntheticRunHasExpectedCountsAndBoundedSize() throws {
        let durationUs: Int64 = 40 * 60 * 1_000_000
        let end = startUs + durationUs
        var heartRate: [WorkoutSampleV6] = []
        heartRate.reserveCapacity(2_400)
        for index in 0..<2_400 {
            let offset = Int64(index) * 1_000_000
            heartRate.append(WorkoutSampleV6(startUs: startUs + offset, endUs: startUs + offset + 999_999,
                                             value: 120 + Double(index % 10)))
        }
        let seriesTypes = ["HKQuantityTypeIdentifierRunningPower",
                           "HKQuantityTypeIdentifierRunningGroundContactTime",
                           "HKQuantityTypeIdentifierRunningStrideLength",
                           "HKQuantityTypeIdentifierRunningVerticalOscillation"]
        var series: [WorkoutStreamV6] = []
        for type in seriesTypes {
            var points: [WorkoutSampleV6] = []
            points.reserveCapacity(600)
            for index in 0..<600 {
                let offset = Int64(index) * 40_000
                points.append(WorkoutSampleV6(startUs: startUs + offset, endUs: startUs + offset + 30_000, value: 250))
            }
            let unit = type.hasSuffix("Power") ? "W" : "m"
            series.append(WorkoutStreamV6(
                type: type, unit: unit, status: "complete", error: nil, samples: [],
                series: [WorkoutSeriesGroupV6(startUs: startUs, endUs: end, value: 250, points: points)]))
        }
        var routePoints: [RoutePoint] = []
        routePoints.reserveCapacity(600)
        for index in 0..<600 {
            let offset = Int64(index) * 4_000_000
            routePoints.append(RoutePoint(recordedAtUs: startUs + offset, latitude: 31.2,
                                          longitude: 121.4, altitude: 10, speed: 3))
        }
        let routes = [routePoints]
        let (raw, json) = try encode(streams: [WorkoutStreamV6(
            type: "HKQuantityTypeIdentifierHeartRate", unit: "count/min", status: "complete",
            error: nil, samples: heartRate, series: [])] + series,
            routes: routes, endUs: end)
        #expect(json["streams"].list.count == 5)
        #expect(json["streams"].list[0]["samples"].list.count == 2_400)
        #expect(json["streams"].list.dropFirst().allSatisfy { $0["series"].list[0]["points"].list.count == 600 })
        #expect(json["routes"].list[0]["points"].list.count == 600)
        #expect(raw.count <= WorkoutBundleV6.maxBytes)
        #expect(raw.count < 1_048_576)
        print("v6 40-minute synthetic payload bytes=\(raw.count)")
    }
}
