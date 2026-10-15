import PulseKit
import SwiftUI

struct PodRow: View {
    let pod: PodSummary
    let showsNamespace: Bool

    private var subtitle: String {
        var parts = [pod.status, "\(pod.readyContainers)/\(pod.totalContainers)"]
        if pod.restarts > 0 { parts.append(Units.restarts(pod.restarts)) }
        if showsNamespace { parts.insert(pod.namespace, at: 0) }
        return parts.joined(separator: " · ")
    }

    var body: some View {
        HStack(spacing: 12) {
            StatusSymbol(problem: pod.problem ? .warning : nil)
            VStack(alignment: .leading, spacing: 1) {
                Text(pod.name)
                    .lineLimit(1)
                    .truncationMode(.middle)
                Text(subtitle)
                    .font(.footnote)
                    .foregroundStyle(pod.problem ? Color.orange : .secondary)
            }
        }
        .padding(.vertical, 2)
    }
}

struct PodDetailView: View {
    let namespace: String
    let name: String
    @Environment(\.pulse) private var pulse

    var body: some View {
        Live(load: { try await pulse.pod(namespace: namespace, name: name) }) { detail in
            PodDetailList(detail: detail)
        }
        .navigationTitle(name)
        .navigationSubtitle(namespace)
        .navigationBarTitleDisplayMode(.inline)
    }
}

private struct PodDetailList: View {
    let detail: PodDetail

    private var pod: PodSummary { detail.summary }

    var body: some View {
        List {
            Section {
                LabeledContent("Status") {
                    Text(pod.status)
                        .foregroundStyle(pod.problem ? Color.orange : .secondary)
                }
                LabeledContent("Ready") {
                    Text("\(pod.readyContainers) of \(pod.totalContainers)")
                        .monospacedDigit()
                }
                LabeledContent("Restarts") {
                    Text(pod.restarts, format: .number)
                        .foregroundStyle(pod.restarts > 0 ? Color.orange : .secondary)
                }
                if let termination = detail.containers.compactMap(\.lastTermination).first {
                    LabeledContent("Last Exit") {
                        Text("\(termination.reason) · \(termination.exitCode)")
                    }
                }
                NavigationLink(value: Route.node(pod.node)) {
                    LabeledContent("Node", value: pod.node)
                }
                if let owner = pod.owner {
                    LabeledContent("Owner", value: owner)
                }
                if let ip = detail.ip {
                    LabeledContent("IP", value: ip)
                        .textSelection(.enabled)
                }
                LabeledContent("Created") {
                    Text(pod.createdAt, format: .relative(presentation: .named))
                }
            }

            Section("Logs") {
                ForEach(detail.containers) { container in
                    NavigationLink {
                        LogsView(
                            namespace: pod.namespace,
                            pod: pod.name,
                            container: container.name,
                            hasPrevious: container.restarts > 0
                        )
                    } label: {
                        Label(container.name, systemImage: "text.alignleft")
                    }
                }
            }

            Section("Containers") {
                ForEach(detail.containers) { container in
                    ContainerRow(container: container)
                }
            }

            if !detail.initContainers.isEmpty {
                Section("Init Containers") {
                    ForEach(detail.initContainers) { container in
                        ContainerRow(container: container)
                    }
                }
            }

            Section("Conditions") {
                ForEach(detail.conditions) { condition in
                    ConditionRow(condition: condition)
                }
            }
        }
    }
}

private struct ContainerRow: View {
    let container: ContainerInfo

    private var state: (text: String, isProblem: Bool) {
        switch container.state {
        case .running:
            container.ready ? (String(localized: "Running"), false) : (String(localized: "Not Ready"), true)
        case .waiting:
            (container.reason ?? String(localized: "Waiting"), true)
        case .terminated:
            (container.reason ?? String(localized: "Terminated"), container.reason != "Completed")
        case .unknown:
            (String(localized: "Unknown"), true)
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack {
                Text(container.name)
                Spacer()
                Text(state.text)
                    .foregroundStyle(state.isProblem ? Color.orange : .secondary)
            }
            Text(container.image)
                .font(.footnote.monospaced())
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .truncationMode(.middle)
            if let message = container.message, !message.isEmpty {
                Text(message)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .lineLimit(3)
            }
            if let last = container.lastTermination {
                Group {
                    if let finished = last.finishedAt {
                        Text("Last exit \(last.reason), code \(last.exitCode), \(finished.shortRelative)")
                    } else {
                        Text("Last exit \(last.reason), code \(last.exitCode)")
                    }
                }
                .font(.footnote)
                .foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 2)
    }
}
