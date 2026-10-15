import Foundation

extension Components.Schemas.Severity: Comparable {
    private var rank: Int {
        switch self {
        case .info: 0
        case .warning: 1
        case .critical: 2
        }
    }

    public static func < (a: Self, b: Self) -> Bool { a.rank < b.rank }
}

extension Components.Schemas.NodeSummary: Identifiable {
    public var id: String { name }

    /// Something worth flagging: not ready, cordoned, or under pressure.
    public var isHealthy: Bool { ready && schedulable && pressure.isEmpty }
}

extension Components.Schemas.NodeDetail: Identifiable {
    public var id: String { value1.name }
    public var summary: NodeSummary { value1 }
    public var conditions: [Condition] { value2.conditions }
    public var pods: [PodSummary] { value2.pods }
    public var events: [Event] { value2.events }
}

extension Components.Schemas.Workload: Identifiable {
    public var id: String { "\(kind.rawValue)/\(namespace)/\(name)" }

    /// How the backend names this workload in `PodSummary.owner`.
    public var ownerReference: String { "\(kind.rawValue)/\(name)" }
}

extension Components.Schemas.PodSummary: Identifiable {
    public var id: String { "\(namespace)/\(name)" }
}

extension Components.Schemas.PodDetail: Identifiable {
    public var id: String { value1.id }
    public var summary: PodSummary { value1 }
    public var ip: String? { value2.ip }
    public var qosClass: String? { value2.qosClass }
    public var initContainers: [ContainerInfo] { value2.initContainers }
    public var containers: [ContainerInfo] { value2.containers }
    public var conditions: [Condition] { value2.conditions }
}

extension Components.Schemas.ContainerInfo: Identifiable {
    public var id: String { name }
}

extension Components.Schemas.Condition: Identifiable {
    public var id: String { _type }
}

extension Components.Schemas.Problem: Identifiable {
    public var id: String { "\(kind.rawValue)/\(namespace ?? "")/\(name)/\(title)" }
}

extension Components.Schemas.HostStatus: Identifiable {
    public var id: String { host }
}

extension Components.Schemas.Event: Identifiable {
    /// `node2` for `host:node2`; nil for `k8s` and other non-host sources.
    public var hostName: String? {
        source.hasPrefix("host:") ? String(source.dropFirst(5)) : nil
    }
}
