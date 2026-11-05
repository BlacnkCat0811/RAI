import Foundation
import Testing
@testable import SyncCore

private func replayFixture() throws -> (JSON, JSON) {
    let id = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
    let bundle: JSON = .object(["protocolVersion": .int(3), "workoutID": .string(id),
        "datasetID": .string("22222222-2222-4222-8222-222222222222"),
        "datasetEpoch": .string("33333333-3333-4333-8333-333333333333"),
        "collectorEpoch": .string("44444444-4444-4444-8444-444444444444"),
        "workout": .object(["durationUs": .int(60_000_000)]),
        "samples": .array([]), "series": .array([]), "routes": .array([]),
        "completeSampleTypes": .array([]), "completeRoutes": .bool(true),
        "observation": .object(["version": .int(1), "observedAtUs": .int(100),
            "metadata": .object(["syntheticExact": .int(Int64.max)]),
            "coverage": .object(["power": .object(["state": .string("no_records_observed")])]),
            "provenance": .object([:])])])
    var receipt = durableWorkoutACK(id, Wire.hash(try bundle.data())).fields
    receipt["sourceObservation"] = .object(["version": .int(1), "observedAtUs": .int(100)])
    return (bundle, .object(receipt))
}

@Suite struct WorkoutExactReplayTests {
    @Test func observationTimeOnlyReplaysEntireOriginalPayloadAndReceiptIdentity() throws {
        let (original, receipt) = try replayFixture()
        var fields = original.fields, observation = original["observation"].fields
        observation["observedAtUs"] = .int(200); fields["observation"] = .object(observation)
        let rebuilt = try JSON.object(fields).data()
        #expect(rebuilt != (try original.data()))
        let replay = try #require(try WorkoutBundleAssembler.exactReplay(rebuilt: rebuilt, receipt: receipt))
        #expect(replay == (try original.data()))
        #expect(Wire.hash(replay) == receipt["bundleSHA256"].text)
    }

    @Test func changedFactsCoverageSourceSessionOrDeletionsCannotReuseReceipt() throws {
        let (original, receipt) = try replayFixture()
        let changes: [String: JSON] = ["workout": .object(["durationUs": .int(60_000_001)]),
            "samples": .array([.object(["synthetic": .bool(true)])]),
            "series": .array([.object(["synthetic": .bool(true)])]),
            "routes": .array([.object(["synthetic": .bool(true)])]),
            "completeRoutes": .bool(false), "completeSampleTypes": .array([.string("synthetic")]),
            "collectorEpoch": .string("55555555-5555-4555-8555-555555555555"),
            "datasetEpoch": .string("66666666-6666-4666-8666-666666666666"),
            "sampleDeletions": .array([.object(["synthetic": .bool(true)])]),
            "routeDeletions": .array([.string("synthetic")])]
        for (key, value) in changes {
            var fields = original.fields; fields[key] = value
            #expect(try WorkoutBundleAssembler.exactReplay(rebuilt: JSON.object(fields).data(), receipt: receipt) == nil)
        }
        for key in ["metadata", "coverage", "provenance"] {
            var fields = original.fields, observation = original["observation"].fields
            observation[key] = .object(["changed": .bool(true)]); fields["observation"] = .object(observation)
            #expect(try WorkoutBundleAssembler.exactReplay(rebuilt: JSON.object(fields).data(), receipt: receipt) == nil)
        }
    }

    @Test func matchingHashStillRequiresFullDurableReceiptValidation() throws {
        let (original, receipt) = try replayFixture()
        for (key, value) in ["ingestStatus": JSON.string("pending"), "commitRevision": .int(0),
                             "workoutID": .string("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")] {
            var fields = receipt.fields; fields[key] = value
            #expect(throws: SyncFailure.receiptMismatch) {
                try WorkoutBundleAssembler.exactReplay(rebuilt: original.data(), receipt: .object(fields))
            }
        }
        var fields = receipt.fields; fields.removeValue(forKey: "sourceObservation")
        #expect(try WorkoutBundleAssembler.exactReplay(rebuilt: original.data(), receipt: .object(fields)) == nil)
    }
}
