import Foundation

/// One container log line, split into the Kubernetes timestamp and the text,
/// with a best-effort guess at its level so errors can be tinted.
public struct LogLine: Identifiable, Hashable, Sendable {
    public enum Level: Sendable, Hashable {
        case warning, error
    }

    public let id: Int
    public let timestamp: Date?
    public let text: String
    public let level: Level?

    /// `raw` is a line as the API returns it with `timestamps=true`:
    /// `2026-10-15T04:12:01.114567890Z message`.
    public init(id: Int, raw: String) {
        self.id = id
        if let space = raw.firstIndex(of: " "),
           let date = try? LenientISO8601DateTranscoder().decode(String(raw[..<space])) {
            timestamp = date
            text = String(raw[raw.index(after: space)...])
        } else {
            timestamp = nil
            text = raw
        }
        level = Self.level(of: text)
    }

    static func level(of text: String) -> Level? {
        if text.contains(Self.error) { return .error }
        if text.contains(Self.warning) { return .warning }
        return nil
    }

    // Uppercase words (`ERROR`, `[WARN]`), logfmt and JSON level fields, and
    // klog's leading E/W severity (`E1015 04:12:01.114 …`).
    nonisolated(unsafe) private static let error =
        #/\b(?:ERROR|ERR|FATAL|PANIC|CRITICAL|CRIT)\b|level=(?:error|fatal|panic|crit)|"level":\s*"(?:error|fatal|panic|critical)"|^E\d{4} /#
    nonisolated(unsafe) private static let warning =
        #/\b(?:WARN|WARNING)\b|level=warn|"level":\s*"warn(?:ing)?"|^W\d{4} /#
}
