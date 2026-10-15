import Foundation

/// The `pulse://` URLs the app opens: enrollment QR codes and ntfy click
/// actions.
public enum PulseLink: Hashable, Sendable {
    /// `pulse://enroll?url=…&token=…`; parse it with `Enrollment(link:)`.
    case enroll
    /// `pulse://events`: the timeline (digests link here).
    case timeline
    /// `pulse://events/<id>`: one event.
    case event(id: String)

    public init?(_ url: URL) {
        guard url.scheme?.lowercased() == "pulse" else { return nil }
        let path = url.pathComponents.filter { $0 != "/" }
        switch (url.host()?.lowercased(), path.count) {
        case ("enroll", _):
            self = .enroll
        case ("events", 0):
            self = .timeline
        case ("events", 1):
            let id = path[0]
            guard (1...64).contains(id.count), id.allSatisfy({ $0.isASCII && ($0.isLetter || $0.isNumber) }) else {
                return nil
            }
            self = .event(id: id)
        default:
            return nil
        }
    }
}
