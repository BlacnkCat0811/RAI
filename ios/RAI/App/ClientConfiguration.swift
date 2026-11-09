import Foundation
import Security
import SyncCore

struct ClientConfiguration: Codable, Sendable {
    let baseURL: URL
    let collectorEpoch: String
    /// v4 is the default. An explicit `syncProtocolV4 = false` keeps a
    /// configuration on v3; the handshake still decides what the server offers.
    var syncProtocolV4: Bool? = nil
    var v4Enabled: Bool { syncProtocolV4 ?? true }
    /// The UUID ingest lane requires both v4 and the handshake capability.
    /// Explicit false is the client-side emergency switch back to the legacy
    /// bundle lane; it never discards a task that already chose the UUID lane.
    var workoutUUIDIngest: Bool? = nil
    var uuidIngestEnabled: Bool { workoutUUIDIngest ?? true }
    static let account = "RAI.V2.DevelopmentConfiguration"
    struct Secret: Codable { let configuration: ClientConfiguration; let token: String }
    static func validatedSecret(baseURL: String, collectorEpoch: String, token: String) throws -> Secret {
        let origin = baseURL.trimmingCharacters(in: .whitespacesAndNewlines)
        let collector = collectorEpoch.trimmingCharacters(in: .whitespacesAndNewlines)
        let credential = token.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let url = URL(string: origin), let components = URLComponents(url: url, resolvingAgainstBaseURL: false),
              components.user == nil, components.password == nil, components.query == nil, components.fragment == nil,
              components.path.isEmpty || components.path == "/", url.host != nil else { throw SyncFailure.invalid("invalid_api_origin") }
        guard let epoch = UUID(uuidString: collector), epoch.uuidString != "00000000-0000-0000-0000-000000000000" else { throw SyncFailure.invalid("invalid_collector_epoch") }
        guard !credential.isEmpty else { throw SyncFailure.invalid("missing_device_token") }
        guard credential.rangeOfCharacter(from: .whitespacesAndNewlines) == nil else { throw SyncFailure.invalid("device_token_contains_whitespace") }
        if url.scheme != "https" {
            #if DEBUG
            guard url.scheme == "http", url.host == Bundle.main.object(forInfoDictionaryKey: "RAIDevelopmentHTTPHost") as? String else { throw SyncFailure.invalid("http_requires_configured_debug_host") }
            #else
            throw SyncFailure.invalid("https_required")
            #endif
        }
        return Secret(configuration: .init(baseURL: url, collectorEpoch: epoch.uuidString.lowercased()), token: credential)
    }
    static func save(baseURL: String, collectorEpoch: String, token: String) throws {
        let secret = try validatedSecret(baseURL: baseURL, collectorEpoch: collectorEpoch, token: token)
        let data = try JSONEncoder().encode(secret)
        let query: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: account, kSecAttrAccount as String: account]
        let update = SecItemUpdate(query as CFDictionary, [kSecValueData as String: data] as CFDictionary)
        if update == errSecItemNotFound {
            var insert = query; insert[kSecValueData as String] = data
            insert[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            let status = SecItemAdd(insert as CFDictionary, nil)
            guard status == errSecSuccess else { throw SyncFailure.localState("keychain_save_failed_\(status)") }
        } else if update != errSecSuccess { throw SyncFailure.localState("keychain_save_failed_\(update)") }
    }

    static func sessionRoot() throws -> URL {
        try FileManager.default.url(for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil, create: true).appendingPathComponent("RAIV2Sync", isDirectory: true)
    }
    static func activeDescriptor() throws -> SyncSessionDescriptor? {
        let m = try SyncSessionStore.read(root: sessionRoot())
        return m.activeSessionID.flatMap { m.sessions[$0] }
    }
    static func load() throws -> Secret? {
        guard let d = try activeDescriptor(), let token = try SessionKeychain().load(sessionID: d.sessionID) else { return nil }
        return Secret(configuration: .init(baseURL: d.baseURL, collectorEpoch: d.collectorEpoch), token: token)
    }
    static func load(sessionID: String) throws -> Secret? {
        let m = try SyncSessionStore.read(root: sessionRoot())
        guard let d = m.sessions[sessionID], let token = try SessionKeychain().load(sessionID: sessionID) else { return nil }
        return Secret(configuration: .init(baseURL: d.baseURL, collectorEpoch: d.collectorEpoch), token: token)
    }
    static func loadLegacy() throws -> Secret? {
        var value: CFTypeRef?
        let query: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: account,
                                   kSecAttrAccount as String: account, kSecReturnData as String: true, kSecMatchLimit as String: kSecMatchLimitOne]
        let status = SecItemCopyMatching(query as CFDictionary, &value)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = value as? Data else { throw SyncFailure.localState("keychain_read_failed") }
        return try JSONDecoder().decode(Secret.self, from: data)
    }
}

/// One immutable Keychain account per session. Existing legacy credentials stay untouched.
struct SessionKeychain: SessionCredentialStore {
    private let service = "RAI.V3.SyncSession"
    func save(token: String, sessionID: String) throws {
        if let existing = try load(sessionID: sessionID) {
            guard existing == token else { throw SyncFailure.localState("session_credential_conflict") }; return
        }
        let query: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service,
            kSecAttrAccount as String: sessionID, kSecValueData as String: Data(token.utf8),
            kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly]
        guard SecItemAdd(query as CFDictionary, nil) == errSecSuccess else { throw SyncFailure.localState("session_keychain_save_failed") }
    }
    func load(sessionID: String) throws -> String? {
        var value: CFTypeRef?
        let query: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service,
            kSecAttrAccount as String: sessionID, kSecReturnData as String: true, kSecMatchLimit as String: kSecMatchLimitOne]
        let status = SecItemCopyMatching(query as CFDictionary, &value)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = value as? Data, let token = String(data: data, encoding: .utf8) else { throw SyncFailure.localState("session_keychain_read_failed_\(status)") }
        return token
    }
}
