import PulseKit
import SwiftUI

// Color carries state only. Healthy is the neutral secondary color (the mint
// tint sits too close to systemGreen to also mean "fine"); orange and red are
// reserved for warning and critical.

extension Severity {
    var tint: Color {
        switch self {
        case .info: .secondary
        case .warning: .orange
        case .critical: .red
        }
    }

    var symbol: String {
        switch self {
        case .info: "info.circle.fill"
        case .warning: "exclamationmark.triangle.fill"
        case .critical: "xmark.octagon.fill"
        }
    }

    var label: String {
        switch self {
        case .info: "Info"
        case .warning: "Warning"
        case .critical: "Critical"
        }
    }
}

extension Usage {
    var fraction: Double { min(max(percent / 100, 0), 1) }

    var tint: Color {
        switch percent {
        case 95...: .red
        case 80...: .orange
        default: .accentColor
        }
    }
}

extension Event {
    var symbol: String {
        if severity != .info { return severity.symbol }
        switch kind {
        case let k where k.hasSuffix("upgrade"): return "arrow.down.circle"
        case "boot": return "power"
        case "ssh-login": return "terminal"
        case "node-health": return "stethoscope"
        case "pod-crashloop": return "arrow.clockwise"
        case "node-notready": return "server.rack"
        case let k where k.hasPrefix("k8s"): return "shippingbox"
        default: return "circle.dashed"
        }
    }

    /// `k8s` → Kubernetes, `host:node2` → node2.
    var sourceLabel: String {
        if let hostName { return hostName }
        return source == "k8s" ? "Kubernetes" : source
    }
}

enum Units {
    /// 1500 → "1.5"
    static func cores(_ millicores: Int64) -> String {
        (Double(millicores) / 1000).formatted(.number.precision(.fractionLength(0...1)))
    }

    static func bytes(_ bytes: Int64) -> String {
        bytes.formatted(.byteCount(style: .memory))
    }

    /// "5.2 of 46.9 GB": one unit, so it fits half a row.
    static func memory(_ usage: Usage) -> String {
        let gb = { (b: Int64) in (Double(b) / 1_073_741_824).formatted(.number.precision(.fractionLength(1))) }
        return String(localized: "\(gb(usage.used)) of \(gb(usage.capacity)) GB")
    }

    static func cpu(_ usage: Usage) -> String {
        String(localized: "\(cores(usage.used)) of \(cores(usage.capacity))")
    }

    /// "1 restart", "3 restarts". Inflection markup only renders inside
    /// AttributedString, not a plain String(localized:).
    static func restarts(_ count: Int) -> String {
        String(AttributedString(localized: "^[\(count) restart](inflect: true)").characters)
    }
}

extension Date {
    /// "2 min ago", or "4:03 AM" / "Oct 12" once it's more than an hour old.
    var shortRelative: String {
        let age = Date.now.timeIntervalSince(self)
        if age < 60 { return String(localized: "now") }
        if age < 3600 { return formatted(.relative(presentation: .numeric, unitsStyle: .abbreviated)) }
        if Calendar.current.isDateInToday(self) { return formatted(date: .omitted, time: .shortened) }
        return formatted(.dateTime.month(.abbreviated).day())
    }
}
