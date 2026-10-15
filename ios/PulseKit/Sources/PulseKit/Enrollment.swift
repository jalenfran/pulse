import Foundation

/// Where the app talks to and how it authenticates. Comes from the QR that
/// `pulse token create <name>` prints: `pulse://enroll?url=…&token=…`.
public struct Enrollment: Codable, Hashable, Sendable {
    public var serverURL: URL
    public var token: String

    public init(serverURL: URL, token: String) {
        self.serverURL = serverURL
        self.token = token
    }

    public enum Invalid: LocalizedError, Equatable {
        case notAnEnrollmentLink
        case badServerURL
        case insecureServerURL
        case badToken

        public var errorDescription: String? {
            switch self {
            case .notAnEnrollmentLink: "That isn't a Pulse enrollment link."
            case .badServerURL: "The server address isn't a valid URL."
            case .insecureServerURL: "The server address must use HTTPS."
            case .badToken: "Tokens start with “pls_”."
            }
        }
    }

    /// Parses a `pulse://enroll` link.
    public init(link: URL) throws(Invalid) {
        guard link.scheme?.lowercased() == "pulse", link.host()?.lowercased() == "enroll",
              let items = URLComponents(url: link, resolvingAgainstBaseURL: false)?.queryItems,
              let server = items.first(where: { $0.name == "url" })?.value,
              let token = items.first(where: { $0.name == "token" })?.value
        else { throw .notAnEnrollmentLink }
        try self.init(server: server, token: token)
    }

    /// Validates what someone typed or pasted.
    public init(server: String, token: String) throws(Invalid) {
        var server = server.trimmingCharacters(in: .whitespacesAndNewlines)
        if !server.contains("://") { server = "https://" + server }
        while server.hasSuffix("/") { server.removeLast() }
        guard let url = URL(string: server), let host = url.host(), !host.isEmpty else {
            throw .badServerURL
        }
        switch url.scheme?.lowercased() {
        case "https": break
        // Plain HTTP only for a backend running on this machine during development.
        case "http" where ["localhost", "127.0.0.1", "::1"].contains(host): break
        case "http": throw .insecureServerURL
        default: throw .badServerURL
        }

        let token = token.trimmingCharacters(in: .whitespacesAndNewlines)
        guard token.hasPrefix("pls_"), token.count > 4,
              token.allSatisfy({ $0.isASCII && ($0.isLetter || $0.isNumber || "_-".contains($0)) })
        else { throw .badToken }

        self.init(serverURL: url, token: token)
    }
}
