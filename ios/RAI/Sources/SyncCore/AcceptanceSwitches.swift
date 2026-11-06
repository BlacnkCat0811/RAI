import Foundation

// Internal rollback switches for the local acceptance harness only.
//
// The implementation plan requires a rollback path per optimisation stage, and
// the acceptance runs need a same-method baseline on the same device. Both are
// served by these switches: they are read once from the process environment,
// they are never exposed in the product UI, and they cannot disable a
// correctness check — only the batching and encoding strategies change, and the
// resulting bundle bytes are identical either way.
//
// A non-DEBUG build always reports `false`, so a shipped app can never enter a
// legacy path by accident.
public enum AcceptanceSwitches {
    /// P2: capture and persist one record (and one route/series point) per
    /// flush instead of the batched 512 records / 256 KiB / 250 ms policy.
    public static let legacyCaptureIO: Bool = read("RAI_ACCEPTANCE_LEGACY_CAPTURE_IO")

    /// P3: encode canonical JSON through `JSONEncoder` plus the post-pass
    /// escapes instead of the single bounded byte writer.
    public static let legacyEncoder: Bool = read("RAI_ACCEPTANCE_LEGACY_ENCODER")

    private static func read(_ name: String) -> Bool {
        #if DEBUG
        return ProcessInfo.processInfo.environment[name] == "1"
        #else
        return false
        #endif
    }
}
