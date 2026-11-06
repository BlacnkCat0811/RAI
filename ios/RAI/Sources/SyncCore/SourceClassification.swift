import Foundation

/// Durable, minimal record of one source-policy decision.
///
/// The decision is made from the root Workout's HealthKit source descriptor
/// *before* any quantity/series/route read. The record keeps only what the
/// frozen policy needs to be re-run and re-proven: hashed Workout identity,
/// policy and classifier versions, the descriptor digest and the protected
/// descriptor evidence. It carries no health value, no device identifier and no
/// GPS data, and it never enters an ordinary log line.
///
/// Reuse is deliberately narrow: a record may only answer for the *same*
/// descriptor digest and the *same* policy version. Any descriptor change
/// (new source, new product type, new device fields) forces a fresh decision
/// instead of trusting an old cache entry.
public enum SourceClassification {
    public static let filePrefix = "classification-"
    public static let fileSuffix = ".json"

    /// Deterministic file name derived from the hashed Workout identity, so the
    /// record needs no index and no rewrite of `state.json`.
    public static func fileName(workoutID: String) -> String {
        filePrefix + WorkoutSourcePolicy.identitySHA256(workoutID) + fileSuffix
    }

    /// The decision record. `descriptorSHA256` is the digest of exactly the
    /// evidence the server would receive, so a mismatch invalidates reuse.
    public static func record(workoutID: String, classification: String, evidence: JSON, now: Date = Date()) throws -> JSON {
        let outcome = WorkoutSourcePolicy.outcome(classification)
        let digest = Wire.hash(try evidence.data())
        return .object([
            "workoutIdentitySHA256": .string(WorkoutSourcePolicy.identitySHA256(workoutID)),
            "classification": .string(classification),
            "outcome": .string(outcome),
            "reason": .string(WorkoutSourcePolicy.reason(outcome)),
            "classificationVersion": .int(WorkoutSourcePolicy.classificationVersion),
            "sourcePolicyVersion": .string(WorkoutSourcePolicy.version),
            "descriptorSHA256": .string(digest),
            "evidenceSHA256": .string(digest),
            "sourceEvidence": evidence,
            "decidedAtUs": .int(Int64(now.timeIntervalSince1970 * 1_000_000)),
        ])
    }
}

public extension OutboxStore {
    /// The last decision for this Workout, or nil when it was never classified.
    func sourceClassification(workoutID: String) -> JSON? {
        guard let url = try? fileURL(SourceClassification.fileName(workoutID: workoutID)),
              let data = try? Data(contentsOf: url) else { return nil }
        return try? JSON.decode(data)
    }

    /// Persist a decision. An identical decision (same descriptor digest, same
    /// policy version, same outcome) is not rewritten, so a repeated scan does
    /// not churn the Outbox. A changed descriptor always writes a new record.
    @discardableResult
    func recordSourceClassification(workoutID: String, classification: String, evidence: JSON) throws -> JSON {
        let next = try SourceClassification.record(workoutID: workoutID, classification: classification, evidence: evidence)
        if let current = sourceClassification(workoutID: workoutID),
           current["descriptorSHA256"] == next["descriptorSHA256"],
           current["sourcePolicyVersion"] == next["sourcePolicyVersion"],
           current["classificationVersion"] == next["classificationVersion"],
           current["outcome"] == next["outcome"] {
            return current
        }
        try Self.write(next.data(), to: try fileURL(SourceClassification.fileName(workoutID: workoutID)))
        return next
    }
}
