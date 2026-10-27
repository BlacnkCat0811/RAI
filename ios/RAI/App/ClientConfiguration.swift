import Foundation
import Security
import SyncCore

struct ClientConfiguration: Codable, Sendable {
    let baseURL: URL
    let collectorEpoch: String
    static let account = "RAI.V2.DevelopmentConfiguration"
    struct Secret: Codable { let configuration: ClientConfiguration; let token: String }
    static func save(baseURL: String, collectorEpoch: String, token: String) throws {
        guard let url = URL(string: baseURL), let components = URLComponents(url: url, resolvingAgainstBaseURL: false),
              components.user == nil, components.password == nil, components.query == nil, components.fragment == nil,
              components.path.isEmpty || components.path == "/", url.host != nil,
              let epoch = UUID(uuidString: collectorEpoch), epoch.uuidString != "00000000-0000-0000-0000-000000000000", !token.isEmpty else { throw SyncFailure.invalid("invalid_configuration") }
        if url.scheme != "https" {
            #if DEBUG
            guard url.scheme == "http", url.host == Bundle.main.object(forInfoDictionaryKey: "RAIDevelopmentHTTPHost") as? String else { throw SyncFailure.invalid("http_requires_configured_debug_host") }
            #else
            throw SyncFailure.invalid("https_required")
            #endif
        }
        let secret = Secret(configuration: .init(baseURL: url, collectorEpoch: epoch.uuidString.lowercased()), token: token)
        let data = try JSONEncoder().encode(secret)
        let query: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: account, kSecAttrAccount as String: account]
        let update = SecItemUpdate(query as CFDictionary, [kSecValueData as String: data] as CFDictionary)
        if update == errSecItemNotFound {
            var insert = query; insert[kSecValueData as String] = data
            insert[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            guard SecItemAdd(insert as CFDictionary, nil) == errSecSuccess else { throw SyncFailure.localState("keychain_save_failed") }
        } else if update != errSecSuccess { throw SyncFailure.localState("keychain_save_failed") }
    }
    static func load() throws -> Secret? {
        var value: CFTypeRef?
        let query: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: account,
                                   kSecAttrAccount as String: account, kSecReturnData as String: true, kSecMatchLimit as String: kSecMatchLimitOne]
        let status = SecItemCopyMatching(query as CFDictionary, &value)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = value as? Data else { throw SyncFailure.localState("keychain_read_failed") }
        return try JSONDecoder().decode(Secret.self, from: data)
    }
}
