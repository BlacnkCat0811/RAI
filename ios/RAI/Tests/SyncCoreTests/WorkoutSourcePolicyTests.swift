import Foundation
import Testing
@testable import SyncCore

private let policyFixture: URL = URL(fileURLWithPath: #filePath)
    .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
    .deletingLastPathComponent().deletingLastPathComponent().appendingPathComponent("contracts/fixtures")

@Suite struct WorkoutSourcePolicyTests {
    /// The shared golden vectors are the only place the frozen policy is
    /// written down; Swift and Go must agree on every one of them.
    @Test func sharedGoldenVectorsClassifyIdentically() throws {
        let raw = try Data(contentsOf: policyFixture.appendingPathComponent("workout-source-policy-v1.json"))
        let expected = try String(contentsOf: policyFixture.appendingPathComponent("workout-source-policy-v1.sha256"), encoding: .utf8)
            .trimmingCharacters(in: .whitespacesAndNewlines)
        #expect(Wire.hash(raw) == expected)
        #expect(WorkoutSourcePolicy.sha256 == expected)
        let document = try JSON.decode(raw)
        #expect(document["policyVersion"].text == WorkoutSourcePolicy.version)
        #expect(document["classificationVersion"].integer == WorkoutSourcePolicy.classificationVersion)
        #expect(document["eligibleClassifications"].list == [.string("apple_watch")])
        let vectors = document["vectors"].list
        #expect(vectors.count == 11)
        for vector in vectors {
            let name = vector["name"].text ?? "?"
            let classification = HealthSourcePolicy.classification(vector["evidence"])
            #expect(classification == vector["classification"].text, "vector \(name)")
            #expect(WorkoutSourcePolicy.outcome(classification) == vector["outcome"].text, "vector \(name)")
            #expect(WorkoutSourcePolicy.reason(WorkoutSourcePolicy.outcome(classification)) == WorkoutSourcePolicy.reason(vector["outcome"].text ?? ""), "vector \(name)")
            // Only Apple Watch native running is ever eligible.
            if classification != "apple_watch" {
                #expect(WorkoutSourcePolicy.outcome(classification) != WorkoutSourcePolicy.eligible, "vector \(name)")
            }
        }
    }
    /// The evidence a client uploads is bounded, canonical and identity-free.
    @Test func evidenceIsBoundedAndCarriesNoWorkoutIdentity() throws {
        let source = JSON.object(["bundleID": .string("com.strava.Strava"), "productType": .string("iPhone14,5"),
            "device": .object(["manufacturer": .string(""), "model": .string("")])])
        let evidence = WorkoutSourcePolicy.evidence(source)
        #expect(evidence["device"].fields.count == 5)
        #expect(evidence["name"].text == "")
        #expect(try evidence.data().count <= 2048)
        let workoutID = UUID().uuidString
        let entry = try WorkoutSourcePolicy.exclusion(workoutID: workoutID, classification: "third_party", evidence: evidence)
        #expect(entry["workoutIdentitySHA256"].text == WorkoutSourcePolicy.identitySHA256(workoutID))
        #expect(entry["outcome"].text == WorkoutSourcePolicy.excluded)
        #expect(entry["reason"].text == WorkoutSourcePolicy.excludedReason)
        #expect(entry["evidenceSHA256"].text == Wire.hash(try evidence.data()))
        #expect(!(try entry.data()).contains(Data(workoutID.utf8)))
    }
    /// Inventory identity is membership, not order.
    @Test func inventoryIdentityIsPermutationStable() {
        let ids = [UUID().uuidString, UUID().uuidString, UUID().uuidString]
        #expect(WorkoutSourcePolicy.inventorySHA256(ids) == WorkoutSourcePolicy.inventorySHA256(ids.reversed()))
        #expect(WorkoutSourcePolicy.inventorySHA256(ids) != WorkoutSourcePolicy.inventorySHA256(Array(ids.dropLast())))
    }
}
