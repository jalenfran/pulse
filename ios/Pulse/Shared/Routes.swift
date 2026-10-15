import PulseKit
import SwiftUI

/// Every push destination, so all four tabs share one set of detail screens.
enum Route: Hashable {
    case node(String)
    case pod(namespace: String, name: String)
    case workload(namespace: String, name: String, kind: Workload.KindPayload?)
    case event(Event)
    /// From a notification link; loaded on arrival.
    case eventID(String)
}

extension View {
    func pulseDestinations() -> some View {
        navigationDestination(for: Route.self) { route in
            switch route {
            case .node(let name):
                NodeDetailView(name: name)
            case .pod(let namespace, let name):
                PodDetailView(namespace: namespace, name: name)
            case .workload(let namespace, let name, let kind):
                WorkloadDetailView(namespace: namespace, name: name, kind: kind)
            case .event(let event):
                EventDetailView(event: event)
            case .eventID(let id):
                EventLinkView(id: id)
            }
        }
    }
}

extension Problem {
    var route: Route? {
        switch kind {
        case .node: .node(name)
        case .pod: namespace.map { .pod(namespace: $0, name: name) }
        case .workload: namespace.map { .workload(namespace: $0, name: name, kind: nil) }
        case .job: namespace.map { .workload(namespace: $0, name: name, kind: .cronJob) }
        }
    }
}
