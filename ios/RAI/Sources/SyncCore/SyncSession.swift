import Foundation

public enum SyncSessionStatus: String, Codable, Sendable { case prepared, active, retired, recoveryRequired = "recovery_required" }
public struct SyncSessionDescriptor: Codable, Equatable, Sendable, Identifiable {
    public let sessionID: String
    public let datasetID: String
    public let serviceID: String
    public let datasetEpoch: String
    public let deviceID: String
    public let collectorEpoch: String
    public let protocolVersion: Int
    public let baseURL: URL
    public let createdAt: Date
    public var status: SyncSessionStatus
    public var id: String { sessionID }
    public var scope: Scope { Scope(serviceID: serviceID, datasetID: datasetID, datasetEpoch: datasetEpoch, collectorEpoch: collectorEpoch) }
    public var backgroundIdentifier: String { "com.rai.v2.sync." + sessionID }
    public init(sessionID: String = Wire.id(), datasetID: String, serviceID: String, datasetEpoch: String, deviceID: String, collectorEpoch: String, baseURL: URL, createdAt: Date = Date()) {
        self.sessionID = sessionID; self.datasetID = datasetID; self.serviceID = serviceID; self.datasetEpoch = datasetEpoch
        self.deviceID = deviceID; self.collectorEpoch = collectorEpoch; self.baseURL = baseURL; self.createdAt = createdAt
        protocolVersion = 3; status = .prepared
    }
    public func validateHandshake(_ hello: JSON) throws {
        guard hello["protocolVersion"].integer == 3, hello["datasetID"].text == datasetID,
              hello["datasetEpoch"].text == datasetEpoch, hello["serviceID"].text == serviceID,
              hello["deviceID"].text == deviceID, hello["collectorEpoch"].text == collectorEpoch,
              hello["datasetStatus"].text == "active", hello["capabilities"]["workoutBundles"] == .bool(true),
              hello["capabilities"]["bundleProtocolVersion"].integer == 3 else { throw SyncFailure.invalid("session_handshake_mismatch") }
    }
}
public protocol SessionCredentialStore: Sendable {
    func save(token: String, sessionID: String) throws
    func load(sessionID: String) throws -> String?
}
public struct SyncSessionManifest: Codable, Sendable {
    public var version = 1
    public var activeSessionID: String?
    public var sessions: [String: SyncSessionDescriptor] = [:]
}
/// A single atomic manifest owns both the active pointer and all lifecycle states.
/// Outboxes and Keychain entries are immutable namespaces, never moved or cleared.
public actor SyncSessionStore {
    public nonisolated let root: URL
    private let credentials: any SessionCredentialStore
    private var activating = false
    public init(root: URL, credentials: any SessionCredentialStore) throws {
        self.root = root; self.credentials = credentials
        try FileManager.default.createDirectory(at: root.appendingPathComponent("sessions"), withIntermediateDirectories: true)
        _ = try Self.read(root: root)
    }
    public nonisolated static func read(root: URL) throws -> SyncSessionManifest {
        let file = root.appendingPathComponent("sessions.json")
        guard FileManager.default.fileExists(atPath: file.path) else { return SyncSessionManifest() }
        let m = try JSONDecoder().decode(SyncSessionManifest.self, from: Data(contentsOf: file))
        guard m.version == 1, m.sessions.allSatisfy({ $0.key == $0.value.sessionID }),
              m.sessions.values.filter({ $0.status == .active }).count == (m.activeSessionID == nil ? 0 : 1),
              m.activeSessionID == nil || m.sessions[m.activeSessionID!]?.status == .active else { throw SyncFailure.localState("invalid_session_manifest") }
        return m
    }
    public nonisolated static func directory(root: URL, sessionID: String) throws -> URL {
        guard UUID(uuidString: sessionID)?.uuidString.lowercased() == sessionID else { throw SyncFailure.invalid("invalid_session_id") }
        return root.appendingPathComponent("sessions", isDirectory: true).appendingPathComponent(sessionID, isDirectory: true)
    }
    private func persist(_ manifest: SyncSessionManifest) throws {
        try OutboxStore.write(JSONEncoder().encode(manifest), to: root.appendingPathComponent("sessions.json"))
    }
    public func snapshot() throws -> SyncSessionManifest { try Self.read(root: root) }
    @discardableResult public func prepare(_ descriptor: SyncSessionDescriptor, token: String, recovery: Bool = false) async throws -> SyncSessionDescriptor {
        var m = try Self.read(root: root)
        _ = try Self.directory(root: root, sessionID: descriptor.sessionID)
        for id in [descriptor.datasetID, descriptor.serviceID, descriptor.datasetEpoch, descriptor.deviceID, descriptor.collectorEpoch] {
            guard let uuid = UUID(uuidString: id), uuid.uuidString.lowercased() == id,
                  id != "00000000-0000-0000-0000-000000000000" else { throw SyncFailure.invalid("invalid_session_identity") }
        }
        let legacyFile = root.appendingPathComponent("state.json")
        if FileManager.default.fileExists(atPath: legacyFile.path) {
            let legacy = try JSONDecoder().decode(LocalState.self, from: Data(contentsOf: legacyFile))
            if let scope = legacy.scope {
                guard scope.datasetID != descriptor.datasetID, scope.datasetEpoch != descriptor.datasetEpoch,
                      scope.collectorEpoch != descriptor.collectorEpoch else { throw SyncFailure.invalid("legacy_session_identity_reused") }
            }
        }
        guard !token.isEmpty, descriptor.protocolVersion == 3, descriptor.status == .prepared else { throw SyncFailure.invalid("invalid_session_prepare") }
        if let existing = m.sessions[descriptor.sessionID] {
            guard existing.scope == descriptor.scope, existing.deviceID == descriptor.deviceID, existing.baseURL == descriptor.baseURL,
                  try credentials.load(sessionID: descriptor.sessionID) == token else { throw SyncFailure.localState("session_identity_conflict") }
            return existing
        }
        for existing in m.sessions.values {
            guard existing.datasetID != descriptor.datasetID, existing.datasetEpoch != descriptor.datasetEpoch,
                  existing.deviceID != descriptor.deviceID, existing.collectorEpoch != descriptor.collectorEpoch,
                  try credentials.load(sessionID: existing.sessionID) != token else { throw SyncFailure.invalid("session_identity_reused") }
        }
        // The durable descriptor precedes Outbox initialization; relaunch resumes it.
        try credentials.save(token: token, sessionID: descriptor.sessionID)
        if recovery, let old = m.activeSessionID {
            m.sessions[old]?.status = .recoveryRequired; m.activeSessionID = nil
        }
        m.sessions[descriptor.sessionID] = descriptor; try persist(m)
        let outbox = try OutboxStore(directory: Self.directory(root: root, sessionID: descriptor.sessionID))
        try await outbox.bind(descriptor.scope)
        return descriptor
    }
    public func activate(_ sessionID: String, handshake: JSON) async throws {
        guard !activating else { throw SyncFailure.localState("session_activation_in_progress") }
        activating = true; defer { activating = false }
        var m = try Self.read(root: root)
        guard var descriptor = m.sessions[sessionID], [.prepared, .active].contains(descriptor.status),
              try credentials.load(sessionID: sessionID) != nil else { throw SyncFailure.localState("session_not_prepared") }
        try descriptor.validateHandshake(handshake)
        let store = try OutboxStore(directory: Self.directory(root: root, sessionID: sessionID))
        try await store.bind(descriptor.scope)
        // No await between the current manifest read and its atomic replacement.
        m = try Self.read(root: root)
        guard let current = m.sessions[sessionID], [.prepared, .active].contains(current.status) else { throw SyncFailure.localState("session_not_prepared") }
        for (id, old) in m.sessions where old.status == .recoveryRequired && id != sessionID { m.sessions[id]?.status = .retired }
        if let old = m.activeSessionID, old != sessionID { m.sessions[old]?.status = .retired }
        descriptor.status = .active; m.sessions[sessionID] = descriptor; m.activeSessionID = sessionID
        try persist(m)
    }
    /// Only an explicit operator declaration or validated identity mismatch calls this.
    /// HTTP status, timeout and connectivity errors never enter this transition.
    public func declareRecoveryRequired(_ sessionID: String) throws {
        var m = try Self.read(root: root)
        guard let d = m.sessions[sessionID], [.active, .recoveryRequired].contains(d.status) else { throw SyncFailure.localState("session_not_active") }
        m.sessions[sessionID]?.status = .recoveryRequired
        if m.activeSessionID == sessionID { m.activeSessionID = nil }
        try persist(m)
    }
    public func validateActiveHandshake(_ sessionID: String, handshake: JSON) throws {
        let d = try requireActive(sessionID)
        do { try d.validateHandshake(handshake) }
        catch { try declareRecoveryRequired(sessionID); throw error }
    }
    public func requireActive(_ sessionID: String) throws -> SyncSessionDescriptor {
        let m = try Self.read(root: root)
        guard m.activeSessionID == sessionID, let d = m.sessions[sessionID], d.status == .active else { throw SyncFailure.localState("session_not_active") }
        return d
    }
    /// Resolve a late callback using its immutable identifier, never the active pointer.
    public func resolveBackground(_ identifier: String) throws -> SyncSessionDescriptor? {
        try Self.read(root: root).sessions.values.first { $0.backgroundIdentifier == identifier }
    }
}

/// Operator private JSON package. Token is consumed only by the session Keychain.
public struct RecoveryCredentialPackage: Decodable, Sendable {
    public struct Identity: Decodable, Sendable {
        public let datasetID, datasetEpoch, deviceID, collectorEpoch, serviceID, token: String
    }
    public let operationID, operationType, baseURL: String
    public let protocolVersion: Int
    public let enrollment: Identity
    public func descriptor() throws -> SyncSessionDescriptor {
        guard operationType == "disaster_recovery", protocolVersion == 3,
              UUID(uuidString: operationID)?.uuidString.lowercased() == operationID,
              let url = URL(string: baseURL), url.host != nil, url.user == nil, url.password == nil,
              url.query == nil, url.fragment == nil, url.path.isEmpty || url.path == "/",
              url.scheme == "https" || (url.scheme == "http" && ["127.0.0.1", "localhost"].contains(url.host!)),
              !enrollment.token.isEmpty else { throw SyncFailure.invalid("invalid_recovery_package") }
        return SyncSessionDescriptor(sessionID: operationID, datasetID: enrollment.datasetID, serviceID: enrollment.serviceID,
            datasetEpoch: enrollment.datasetEpoch, deviceID: enrollment.deviceID, collectorEpoch: enrollment.collectorEpoch, baseURL: url)
    }
}
