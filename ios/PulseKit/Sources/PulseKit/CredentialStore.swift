import Foundation
import Security

/// Keeps the enrollment (server URL and token) in the Keychain, available
/// after first unlock so background refresh and widgets can use it later,
/// and never synced off the device.
public enum CredentialStore {
    private static let service = "Pulse"
    private static let account = "enrollment"

    private static var query: [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
    }

    public static func load() -> Enrollment? {
        var q = query
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess, let data = out as? Data else {
            return nil
        }
        return try? JSONDecoder().decode(Enrollment.self, from: data)
    }

    public static func save(_ enrollment: Enrollment) throws {
        let data = try JSONEncoder().encode(enrollment)
        SecItemDelete(query as CFDictionary)
        var q = query
        q[kSecValueData as String] = data
        q[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        let status = SecItemAdd(q as CFDictionary, nil)
        guard status == errSecSuccess else {
            throw NSError(domain: NSOSStatusErrorDomain, code: Int(status))
        }
    }

    public static func delete() {
        SecItemDelete(query as CFDictionary)
    }
}
