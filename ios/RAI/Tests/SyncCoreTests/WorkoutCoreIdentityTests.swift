import Foundation
import Testing
@testable import SyncCore

private func identityWorkout(_ quantity: JSON) -> JSON {
    let q = JSON.object(["value": quantity, "unit": .string("m")])
    let statistic = JSON.object(["type": .string("HKQuantityTypeIdentifierDistanceWalkingRunning"),
        "startUs": .int(1_700_000_000_000_000), "endUs": .int(1_700_000_060_000_000), "sum": q])
    return .object(["uuid": .string("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
        "startUs": .int(1_700_000_000_000_000), "endUs": .int(1_700_000_060_000_000),
        "workout": .object(["activityType": .int(37), "durationUs": .int(60_000_000),
            "totalDistance": q, "statistics": .array([statistic]),
            "activities": .array([.object(["durationUs": .int(60_000_000),
                "configuration": .object(["activityType": .int(37), "lapLength": q]),
                "statistics": .array([statistic])])])]),
        "metadata": .object(["syntheticExactInteger": .int(Int64.max)])])
}

@Suite struct WorkoutCoreIdentityTests {
    @Test func integerCarrierAndFloatingCarrierHaveSameCanonicalIdentity() throws {
        for value: Int64 in [0, 42, 5000, 9_007_199_254_740_992] {
            let integer = identityWorkout(.int(value)), floating = identityWorkout(.number(Double(value)))
            #expect(integer != floating) // Reproduces the pre-fix enum mismatch.
            #expect(try integer.data() == floating.data())
            #expect(try WorkoutCoreIdentity.canonical(integer) == WorkoutCoreIdentity.canonical(floating))
        }
    }

    @Test func encodeDecodeCanonicalIdentityProperty() throws {
        // Fixed, reproducible synthetic property set: integer-looking values,
        // fractions and valid negative quantities (e.g. temperature domains).
        for i in -512...512 {
            for value in [Double(i), Double(i) / 8, Double(i) / 13] {
                let event = identityWorkout(.number(value))
                let decoded = try JSONDecoder().decode(JSON.self, from: JSONEncoder().encode(event))
                let canonical = try WorkoutCoreIdentity.canonical(event)
                #expect(try WorkoutCoreIdentity.canonical(decoded) == canonical)
                #expect(try Wire.hash(WorkoutCoreIdentity.canonical(decoded)) == Wire.hash(canonical))
                #expect(try decoded.data() == event.data())
            }
        }
    }

    @Test func oldPersistedTaskReopensWithoutReplacingOriginalFactsOrAnchors() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-identity-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: directory) }
        let id = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
        let old = try JSON.decode(identityWorkout(.int(42)).data())
        let store = try OutboxStore(directory: directory)
        try await store.beginTask(id)
        try await store.updateTask(id) { $0.workoutAnchor = Data([1, 2, 3]); $0.originalWorkout = old }
        let before = try old.data(), beforeHash = Wire.hash(before)
        let reopened = try OutboxStore(directory: directory)
        let retained = try await reopened.originalWorkout(id, event: identityWorkout(.number(42)))
        #expect(try retained.data() == before)
        #expect(try Wire.hash(retained.data()) == beforeHash)
        #expect(retained["metadata"]["syntheticExactInteger"] == .int(Int64.max))
        #expect(await reopened.task(id)?.workoutAnchor == Data([1, 2, 3]))
        do {
            _ = try await reopened.originalWorkout(id, event: identityWorkout(.number(42.25)))
            Issue.record("Changed quantity was accepted")
        } catch { #expect(error as? SyncFailure == .localState("workout_core_change_requires_review")) }
        #expect(try await reopened.task(id)?.originalWorkout?.data() == before)
    }

    @Test func exactIntegersUnitsTypesAndContentChangesRemainDistinct() throws {
        let base = identityWorkout(.int(42))
        var changed = base.fields
        changed["startUs"] = .int(1_700_000_000_000_001)
        #expect(try WorkoutCoreIdentity.canonical(.object(changed)) != WorkoutCoreIdentity.canonical(base))
        #expect(try WorkoutCoreIdentity.canonical(identityWorkout(.int(Int64.max))) !=
            WorkoutCoreIdentity.canonical(identityWorkout(.number(Double(Int64.max)))))
        #expect(try WorkoutCoreIdentity.canonical(identityWorkout(.int(9_007_199_254_740_993))) !=
            WorkoutCoreIdentity.canonical(identityWorkout(.number(9_007_199_254_740_992))))
        #expect(try WorkoutCoreIdentity.canonical(identityWorkout(.bool(true))) !=
            WorkoutCoreIdentity.canonical(identityWorkout(.int(1))))
        let meters: JSON = .object(["unit": .string("m"), "value": .number(42)])
        let seconds: JSON = .object(["unit": .string("s"), "value": .number(42)])
        #expect(try WorkoutCoreIdentity.canonical(meters) != WorkoutCoreIdentity.canonical(seconds))
        for value in [Double.nan, .infinity, -.infinity] {
            #expect(throws: (any Error).self) { try WorkoutCoreIdentity.canonical(identityWorkout(.number(value))) }
        }
    }

    @Test func quantitySignedZeroDoesNotRewriteWireOrGlobalEquality() throws {
        let negative = identityWorkout(.number(-0.0)), zero = identityWorkout(.int(0))
        #expect(try WorkoutCoreIdentity.canonical(negative) == WorkoutCoreIdentity.canonical(zero))
        #expect(try negative.data() != zero.data())
        #expect(JSON.int(42) != JSON.number(42))
        #expect(try JSON.int(Int64.max).data() == Data("9223372036854775807".utf8))
    }
}
