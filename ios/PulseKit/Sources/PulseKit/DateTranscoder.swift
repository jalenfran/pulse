import Foundation
import OpenAPIRuntime

/// Go writes `time.Time` as RFC 3339 with zero to nine fractional digits
/// (`2026-10-16T04:03:11.123456789Z`, or no fraction at all). Foundation's
/// ISO 8601 parsers want either exactly no fraction or at most millisecond
/// precision, so normalise the fraction to three digits before parsing.
public struct LenientISO8601DateTranscoder: DateTranscoder {
    public init() {}

    public func encode(_ date: Date) throws -> String {
        date.formatted(Self.fractional)
    }

    public func decode(_ string: String) throws -> Date {
        if let date = try? Self.fractional.parse(Self.normalised(string)) {
            return date
        }
        if let date = try? Self.whole.parse(string) {
            return date
        }
        throw DecodingError.dataCorrupted(
            .init(codingPath: [], debugDescription: "Expected an RFC 3339 date-time, got \(string)")
        )
    }

    private static let fractional = Date.ISO8601FormatStyle(includingFractionalSeconds: true)
    private static let whole = Date.ISO8601FormatStyle()

    /// Pads or truncates the fractional seconds to exactly three digits.
    static func normalised(_ string: String) -> String {
        guard let t = string.firstIndex(of: "T") else { return string }
        let time = string[t...]
        guard let dot = time.firstIndex(of: ".") else {
            // No fraction: insert ".000" before the zone designator.
            guard let zone = time.firstIndex(where: { $0 == "Z" || $0 == "+" || $0 == "-" }) else {
                return string
            }
            return string[..<zone] + ".000" + string[zone...]
        }
        let digitsStart = string.index(after: dot)
        let digitsEnd = string[digitsStart...].firstIndex(where: { !$0.isNumber }) ?? string.endIndex
        let digits = string[digitsStart..<digitsEnd]
        let millis = String(digits.prefix(3)).padding(toLength: 3, withPad: "0", startingAt: 0)
        return string[..<digitsStart] + millis + string[digitsEnd...]
    }
}
