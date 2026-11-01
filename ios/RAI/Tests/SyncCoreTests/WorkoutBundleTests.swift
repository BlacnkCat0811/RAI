import XCTest
import SyncCore

final class WorkoutBundleTests: XCTestCase {
    func testBundleIsOneDeterministicProtocol3Document() throws {
        let scope = Scope(serviceID: "11111111-1111-4111-8111-111111111111",
                          datasetID: "22222222-2222-4222-8222-222222222222",
                          datasetEpoch: "33333333-3333-4333-8333-333333333333",
                          collectorEpoch: "44444444-4444-4444-8444-444444444444")
        let workout = JSON.object(["op": .string("upsert"), "kind": .string("workout")])
        let data = try WorkoutBundleAssembler.data(scope: scope, workoutID: "55555555-5555-4555-8555-555555555555",
            workout: workout, samples: [], series: [], routes: [],
            completeSampleTypes: [], completeRoutes: false)
        let value = try JSON.decode(data)
        XCTAssertEqual(value["protocolVersion"].integer, 3)
        XCTAssertEqual(value["datasetID"].text, scope.datasetID)
        XCTAssertEqual(value["workoutID"].text, "55555555-5555-4555-8555-555555555555")
        XCTAssertEqual(value["samples"].list.count, 0)
        XCTAssertEqual(value["completeSampleTypes"].list.count, 0)
        XCTAssertEqual(value["completeRoutes"], .bool(false))
        XCTAssertEqual(try data, try WorkoutBundleAssembler.data(scope: scope, workoutID: "55555555-5555-4555-8555-555555555555",
            workout: workout, samples: [], series: [], routes: [],
            completeSampleTypes: [], completeRoutes: false))
    }

    func testMultipartUsesSingleFileField() throws {
        let body = WorkoutBundleAssembler.multipart(bundle: Data("{}".utf8), boundary: "rai.boundary.test")
        let text = String(decoding: body, as: UTF8.self)
        XCTAssertTrue(text.contains("name=\"file\""))
        XCTAssertTrue(text.contains("filename=\"workout.json\""))
        XCTAssertTrue(text.hasSuffix("--rai.boundary.test--\r\n"))
    }
}
