import Foundation

public struct SyncMetrics: Sendable {
    private let sink: @Sendable (String, Double) -> Void
    public init(_ sink: @escaping @Sendable (String, Double) -> Void = { _, _ in }) { self.sink = sink }
    public func value(_ name: String, _ value: Double) { sink(name, value) }
    public func elapsed(_ name: String, since: Date) { sink(name, Date().timeIntervalSince(since) * 1000) }
}
