import PulseKit
import SwiftUI

struct NodeDetailView: View {
    let name: String
    @Environment(\.pulse) private var pulse

    var body: some View {
        Live(load: { try await pulse.node(name) }) { detail in
            NodeDetailList(detail: detail)
        }
        .navigationTitle(name)
        .navigationBarTitleDisplayMode(.inline)
    }
}

private struct NodeDetailList: View {
    let detail: NodeDetail

    private var node: NodeSummary { detail.summary }

    /// Problem pods first, then by name.
    private var pods: [PodSummary] {
        detail.pods.sorted { ($0.problem ? 0 : 1, $0.namespace, $0.name) < ($1.problem ? 0 : 1, $1.namespace, $1.name) }
    }

    var body: some View {
        List {
            Section {
                LabeledContent("Status") {
                    Text(node.ready ? "Ready" : "NotReady")
                        .foregroundStyle(node.ready ? Color.secondary : .red)
                }
                LabeledContent("Scheduling") {
                    Text(node.schedulable ? "Enabled" : "Cordoned")
                        .foregroundStyle(node.schedulable ? Color.secondary : .orange)
                }
                LabeledContent("Roles", value: node.roleLabel)
                LabeledContent("Internal IP", value: node.internalIP)
                    .textSelection(.enabled)
                LabeledContent("Kubelet", value: node.kubeletVersion)
                LabeledContent("Kernel", value: node.kernelVersion)
                LabeledContent("OS", value: node.osImage)
                LabeledContent("Joined") {
                    Text(node.createdAt, format: .dateTime.year().month().day())
                }
            }

            Section("Usage") {
                UsageBar(label: String(localized: "CPU cores"), usage: node.cpu, format: Units.cpu)
                    .padding(.vertical, 4)
                UsageBar(label: String(localized: "Memory"), usage: node.memory, format: Units.memory)
                    .padding(.vertical, 4)
            }

            Section("Conditions") {
                ForEach(detail.conditions) { condition in
                    ConditionRow(condition: condition)
                }
            }

            Section("Pods") {
                ForEach(pods) { pod in
                    NavigationLink(value: Route.pod(namespace: pod.namespace, name: pod.name)) {
                        PodRow(pod: pod, showsNamespace: true)
                    }
                }
            }

            if !detail.events.isEmpty {
                Section("Host Events") {
                    ForEach(detail.events) { event in
                        NavigationLink(value: Route.event(event)) {
                            EventRow(event: event)
                        }
                    }
                }
            }
        }
    }
}

struct ConditionRow: View {
    let condition: Condition

    /// Ready-style conditions should be True; pressure-style ones False.
    private var isExpected: Bool {
        let positive = ["Ready", "Initialized", "ContainersReady", "PodScheduled", "PodReadyToStartContainers"]
        return positive.contains(condition._type) ? condition.status == "True" : condition.status == "False"
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            LabeledContent(condition._type) {
                Text(condition.status)
                    .foregroundStyle(isExpected ? Color.secondary : .orange)
            }
            if !isExpected, let message = condition.message, !message.isEmpty {
                Text(message)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }
        }
    }
}
