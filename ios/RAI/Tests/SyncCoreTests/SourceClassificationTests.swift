import Foundation
import Testing
@testable import SyncCore

/// The classification record is what a list, a discovery pass and a queued task
/// share, and what an exclusion is later proven from. It must be decided from
/// the descriptor alone, stored without the raw Workout identity, and re-decided
/// whenever the descriptor changes.
@Suite struct SourceClassificationTests {
    func directory() -> URL { FileManager.default.temporaryDirectory.appendingPathComponent("rai-classify-test-" + Wire.id()) }

    func source(bundle: String, product: String? = nil, manufacturer: String? = nil, model: String? = nil,
                name: String? = nil, version: String? = "11.2") -> JSON {
        var device: [String: JSON] = [:]
        if let manufacturer { device["manufacturer"] = .string(manufacturer) }
        if let model { device["model"] = .string(model) }
        var fields: [String: JSON] = ["bundleID": .string(bundle), "device": .object(device)]
        if let product { fields["productType"] = .string(product) }
        if let name { fields["name"] = .string(name) }
        if let version { fields["version"] = .string(version) }
        return .object(fields)
    }

    /// A device nickname is a label, not evidence. Renaming a Watch, or naming a
    /// third-party writer "Apple Watch", cannot change the class.
    @Test func displayNamesNeverChangeClassification() {
        let watch = source(bundle: "com.apple.health", product: "Watch7,3", manufacturer: "Apple", model: "Watch7,3")
        for name in ["Apple Watch", "Ta's Watch", "跑步手表", "", "iPhone"] {
            var fields = watch.fields
            fields["name"] = .string(name)
            var device = watch["device"].fields
            device["name"] = .string(name)
            fields["device"] = .object(device)
            #expect(HealthSourcePolicy.classification(.object(fields)) == "apple_watch", "name \(name.debugDescription)")
        }
        let thirdParty = source(bundle: "com.strava.Strava", product: "Watch7,3", manufacturer: "Apple",
                                model: "Watch7,3", name: "Apple Watch")
        #expect(HealthSourcePolicy.classification(thirdParty) == "third_party")
        #expect(WorkoutSourcePolicy.outcome(HealthSourcePolicy.classification(thirdParty)) == WorkoutSourcePolicy.excluded)
    }

    /// An undecidable descriptor stays unclassified. It is never promoted to
    /// eligible and never silently excluded.
    @Test func contradictoryOrMissingDescriptorsStayUnclassified() {
        let cases: [JSON] = [
            source(bundle: "com.apple.health", manufacturer: "Apple", model: "Watch7,3"),           // no productType
            source(bundle: "com.apple.health", product: "iPhone14,5", manufacturer: "Apple", model: "Watch7,3"),
            source(bundle: "com.apple.health", product: "Watch7,3", manufacturer: "Garmin", model: "Watch7,3"),
            source(bundle: "com.apple.health", product: "Watch7,3", manufacturer: "Apple", model: "iPhone"),
            source(bundle: "com.apple.Health", product: "Watch7,3", manufacturer: "Apple", model: "Watch7,3"),
            .object([:]),
        ]
        for value in cases {
            let classification = HealthSourcePolicy.classification(value)
            #expect(classification == "source_unknown", "\(try! value.data().count) \(classification)")
            #expect(WorkoutSourcePolicy.outcome(classification) == WorkoutSourcePolicy.unclassified)
        }
        // Hand-entered data is the one deterministic exclusion with no device.
        let entered = source(bundle: "com.apple.Health", version: nil)
        #expect(HealthSourcePolicy.classification(entered) == "user_entered")
        #expect(WorkoutSourcePolicy.outcome("user_entered") == WorkoutSourcePolicy.excluded)
    }

    /// The stored record carries hashed identity only, is stable across a
    /// restart, and is replaced when the descriptor or the policy changes.
    @Test func recordIsIdentityFreeAndRedecidedOnChange() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir)
        let workoutID = UUID().uuidString
        let thirdParty = source(bundle: "com.strava.Strava", product: "Watch7,3", name: "Apple Watch")
        let classification = HealthSourcePolicy.classification(thirdParty)
        let evidence = WorkoutSourcePolicy.evidence(thirdParty)
        try await store.recordSourceClassification(workoutID: workoutID, classification: classification, evidence: evidence)

        let file = SourceClassification.fileName(workoutID: workoutID)
        let url = try await store.fileURL(file)
        let bytes = try Data(contentsOf: url)
        #expect(!bytes.contains(Data(workoutID.utf8)), "the record must not carry the raw Workout UUID")
        let record = try JSON.decode(bytes)
        #expect(record["classification"].text == "third_party")
        #expect(record["outcome"].text == WorkoutSourcePolicy.excluded)
        #expect(record["reason"].text == WorkoutSourcePolicy.excludedReason)
        #expect(record["sourcePolicyVersion"].text == WorkoutSourcePolicy.version)
        #expect(record["classificationVersion"].integer == WorkoutSourcePolicy.classificationVersion)
        #expect(record["workoutIdentitySHA256"].text == WorkoutSourcePolicy.identitySHA256(workoutID))
        #expect(record["descriptorSHA256"].text == Wire.hash(try evidence.data()))
        #expect(record["evidenceSHA256"].text == Wire.hash(try record["sourceEvidence"].data()))

        // Same descriptor and policy: the file is not rewritten.
        let stamp = try FileManager.default.attributesOfItem(atPath: url.path)[.modificationDate] as? Date
        try await store.recordSourceClassification(workoutID: workoutID, classification: classification, evidence: evidence)
        #expect(try FileManager.default.attributesOfItem(atPath: url.path)[.modificationDate] as? Date == stamp)

        // A changed descriptor is re-decided instead of trusting the old record.
        let native = source(bundle: "com.apple.health", product: "Watch7,3", manufacturer: "Apple", model: "Watch7,3")
        try await store.recordSourceClassification(workoutID: workoutID,
                                                   classification: HealthSourcePolicy.classification(native),
                                                   evidence: WorkoutSourcePolicy.evidence(native))
        let reopened = try OutboxStore(directory: dir)
        let stored = await reopened.sourceClassification(workoutID: workoutID)
        #expect(stored?["classification"].text == "apple_watch")
        #expect(stored?["outcome"].text == WorkoutSourcePolicy.eligible)
    }

    /// The record is a payload, never part of the durable state machine: a
    /// missing record must not break a store, and a stale record never becomes
    /// an upload decision on its own.
    @Test func missingRecordIsNotADecision() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir)
        #expect(await store.sourceClassification(workoutID: UUID().uuidString) == nil)
    }
}
