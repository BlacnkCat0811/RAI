import Foundation

/// Client-side copy of the server's per-device upload budget (GCRA, the same
/// math the server uses), so a backfill sends at the admitted rate instead of
/// bursting into 429s. Configured from the handshake `limits.requests.upload`;
/// a 429 pushes the next slot past its Retry-After. Shared by Daily and VO2.
public actor UploadPacer {
    public static let defaultPerMinute = 30
    public static let defaultBurst = 2
    /// Requests arrive a little later than they are sent, but network jitter
    /// can bunch them; pace 5% under the advertised rate.
    static let margin = 1.05
    private var interval: TimeInterval
    private var burst: Int
    private var tat = Date.distantPast

    public init(perMinute: Int = UploadPacer.defaultPerMinute, burst: Int = UploadPacer.defaultBurst) {
        (interval, self.burst) = Self.normalized(perMinute: perMinute, burst: burst)
    }

    static func normalized(perMinute: Int, burst: Int) -> (TimeInterval, Int) {
        (60 / Double(min(max(perMinute, 1), 100_000)) * margin, min(max(burst, 1), 100_000))
    }

    /// Budget advertised by the server handshake, or the protocol defaults.
    public static func budget(handshake: JSON) -> (perMinute: Int, burst: Int) {
        let device = handshake["limits"]["requests"]["upload"]["device"]
        guard let perMinute = device["perMinute"].integer, let burst = device["burst"].integer, perMinute > 0, burst > 0 else {
            return (defaultPerMinute, defaultBurst)
        }
        return (Int(perMinute), Int(burst))
    }

    public func configure(perMinute: Int, burst: Int) {
        (interval, self.burst) = Self.normalized(perMinute: perMinute, burst: burst)
    }

    /// Reserves the next upload slot and returns how long to wait before
    /// sending. A reservation is spent even if the caller then gives up, which
    /// only makes later requests more conservative.
    public func reserve(now: Date = Date()) -> TimeInterval {
        tat = max(tat, now).addingTimeInterval(interval)
        return max(0, tat.addingTimeInterval(-Double(burst) * interval).timeIntervalSince(now))
    }

    /// Delay the next reservation until at least `until` (server Retry-After).
    public func penalize(until: Date) {
        tat = max(tat, until.addingTimeInterval(Double(burst - 1) * interval))
    }
}
