import PulseKit
import SwiftUI

struct NodesView: View {
    @Environment(\.pulse) private var pulse

    var body: some View {
        NavigationStack {
            Live(load: pulse.nodes) { nodes in
                List {
                    ForEach(nodes) { node in
                        Section {
                            NavigationLink(value: Route.node(node.name)) {
                                NodeHeaderRow(node: node)
                            }
                            HStack(spacing: 20) {
                                UsageBar(label: String(localized: "CPU"), usage: node.cpu, format: Units.cpu)
                                UsageBar(label: String(localized: "Memory"), usage: node.memory, format: Units.memory)
                            }
                            .padding(.leading, 40)
                            .padding(.vertical, 4)
                        }
                    }
                }
                .listSectionSpacing(.compact)
                .navigationSubtitle(summary(nodes))
            }
            .navigationTitle("Nodes")
            .pulseDestinations()
        }
    }

    private func summary(_ nodes: [NodeSummary]) -> String {
        let ready = nodes.count(where: \.ready)
        let versions = Set(nodes.map(\.kubeletVersion))
        var text = String(localized: "\(ready) of \(nodes.count) Ready")
        if versions.count == 1, let version = versions.first { text += " · \(version)" }
        return text
    }
}

private struct NodeHeaderRow: View {
    let node: NodeSummary

    private var problem: String? {
        if !node.ready { return String(localized: "NotReady") }
        if !node.pressure.isEmpty { return node.pressure.joined(separator: ", ") }
        if !node.schedulable { return String(localized: "Cordoned") }
        return nil
    }

    var body: some View {
        HStack(spacing: 12) {
            StatusSymbol(problem: node.isHealthy ? nil : (node.ready ? .warning : .critical))
            VStack(alignment: .leading, spacing: 1) {
                Text(node.name)
                    .font(.headline)
                Text("\(node.roleLabel) · \(node.internalIP)")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }
            Spacer()
            if let problem {
                Text(problem)
                    .font(.subheadline)
                    .foregroundStyle(node.ready ? .orange : .red)
            }
        }
        .padding(.vertical, 2)
    }
}
