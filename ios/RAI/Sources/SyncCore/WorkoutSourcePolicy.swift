import Foundation

/// RAI Workout Source Policy V1 (UA1-R3B), the client half of the frozen
/// contract. Only an Apple Watch native running Workout is eligible; an
/// explicitly identified third-party or user-entered Workout is excluded by
/// policy and must be *proven* excluded; anything unresolved blocks.
///
/// The policy document is `contracts/fixtures/workout-source-policy-v1.json`
/// and `sha256` is its canonical digest. Both Swift and Go must classify every
/// golden vector identically; neither language may relax the classifier here.
public enum WorkoutSourcePolicy {
    public static let version = "rai.workout.source_policy.apple_watch_only/v1"
    public static let sha256 = "63713b4f82a33c6e9544de37fbd0701c42a29b71f2ca54588c7fc9cf463e0654"
    public static let classificationVersion: Int64 = 1
    public static let eligible = "eligible", excluded = "excluded", unclassified = "unclassified"
    public static let eligibleReason = "eligible_source_policy"
    public static let excludedReason = "excluded_by_source_policy"
    public static let unclassifiedReason = "unclassified_source"

    /// The whole policy. A classification missing from this table is never
    /// eligible and never excluded by accident.
    public static func outcome(_ classification: String) -> String {
        switch classification {
        case "apple_watch": return eligible
        case "third_party", "user_entered": return excluded
        default: return unclassified
        }
    }
    public static func reason(_ outcome: String) -> String {
        switch outcome {
        case eligible: return eligibleReason
        case excluded: return excludedReason
        default: return unclassifiedReason
        }
    }
    /// The only Workout identity that may appear in inventory evidence.
    public static func identitySHA256(_ workoutID: String) -> String {
        Wire.hash(Data(workoutID.lowercased().utf8))
    }
    /// Order-independent digest over hashed identities.
    public static func inventorySHA256(_ workoutIDs: [String]) -> String {
        Wire.hash(Data(("[" + workoutIDs.map(identitySHA256).sorted().map { "\"" + $0 + "\"" }.joined(separator: ",") + "]").utf8))
    }
    /// The minimal source descriptor the classifier reads, with every key
    /// present so the server can re-run the frozen policy deterministically.
    public static func evidence(_ source: JSON) -> JSON {
        func text(_ value: JSON?) -> JSON {
            let value = value?.text.flatMap { $0.isEmpty ? nil : $0 } ?? ""
            return .string(value)
        }
        let device = source["device"]
        return .object([
            "bundleID": text(source["bundleID"]), "name": text(source["name"]), "version": text(source["version"]),
            "productType": text(source["productType"]), "operatingSystemVersion": text(source["operatingSystemVersion"]),
            "device": .object(["manufacturer": text(device["manufacturer"]), "model": text(device["model"]),
                "hardwareVersion": text(device["hardwareVersion"]), "firmwareVersion": text(device["firmwareVersion"]),
                "softwareVersion": text(device["softwareVersion"])])])
    }
    /// One durable exclusion entry. It carries no raw Workout UUID, no device
    /// identifier and no health value: only hashed identity, class, reason and
    /// the minimal descriptor evidence.
    public static func exclusion(workoutID: String, classification: String, evidence: JSON) throws -> JSON {
        .object(["workoutIdentitySHA256": .string(identitySHA256(workoutID)),
            "classification": .string(classification), "outcome": .string(excluded),
            "reason": .string(excludedReason), "classificationVersion": .int(classificationVersion),
            "sourcePolicyVersion": .string(version), "evidenceSHA256": .string(Wire.hash(try evidence.data())),
            "sourceEvidence": evidence])
    }
}
