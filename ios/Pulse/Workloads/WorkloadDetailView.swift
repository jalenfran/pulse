import PulseKit
import SwiftUI

struct WorkloadDetailView: View {
    let namespace: String
    let name: String
    /// Nil when coming from a problem, which doesn't say the kind.
    let kind: Workload.KindPayload?
    @Environment(\.pulse) private var pulse

    var body: some View {
        Live(load: load) { loaded in
            if let workload = loaded.workload {
                WorkloadDetailList(workload: workload, pods: loaded.pods)
            } else {
                ContentUnavailableView(
                    "Not Found",
                    systemImage: "square.stack.3d.up.slash",
                    description: Text("\(namespace)/\(name) no longer exists.")
                )
            }
        }
        .navigationTitle(name)
        .navigationBarTitleDisplayMode(.inline)
    }

    private struct Loaded {
        var workload: Workload?
        var pods: [PodSummary]
    }

    private func load() async throws -> Loaded {
        async let workloads = pulse.workloads(namespace: namespace)
        async let pods = pulse.pods(namespace: namespace)
        guard let workload = try await workloads.first(where: { $0.name == name && (kind == nil || $0.kind == kind) })
        else { return Loaded(workload: nil, pods: []) }
        let owned = try await pods.filter { pod in
            guard let owner = pod.owner else { return false }
            // A CronJob's pods belong to the Jobs it spawns, named "<cronjob>-<time>".
            return workload.kind == .cronJob ? owner.hasPrefix("Job/\(name)-") : owner == workload.ownerReference
        }
        return Loaded(workload: workload, pods: owned.sorted { $0.createdAt > $1.createdAt })
    }
}

private struct WorkloadDetailList: View {
    let workload: Workload
    let pods: [PodSummary]

    var body: some View {
        List {
            Section {
                LabeledContent("Kind", value: workload.kind.rawValue)
                LabeledContent("Namespace", value: workload.namespace)
                if workload.kind == .cronJob {
                    LabeledContent("Last Run") {
                        Text(workload.status)
                            .foregroundStyle(workload.healthy ? Color.secondary : .orange)
                    }
                    if let schedule = workload.schedule {
                        LabeledContent("Schedule") {
                            Text(schedule).monospaced()
                        }
                    }
                    if let last = workload.lastScheduleTime {
                        LabeledContent("Last Scheduled") {
                            Text(last, format: .dateTime.month().day().hour().minute())
                        }
                    }
                } else {
                    LabeledContent("Ready") {
                        Text(workload.status)
                            .monospacedDigit()
                            .foregroundStyle(workload.healthy ? Color.secondary : .orange)
                    }
                    LabeledContent("Restarts") {
                        Text(workload.restarts, format: .number)
                            .foregroundStyle(workload.restarts > 0 ? Color.orange : .secondary)
                    }
                }
            }

            Section("Images") {
                ForEach(workload.images, id: \.self) { image in
                    Text(image)
                        .font(.footnote.monospaced())
                        .textSelection(.enabled)
                }
            }

            Section("Pods") {
                if pods.isEmpty {
                    Text("No pods")
                        .foregroundStyle(.secondary)
                } else {
                    ForEach(pods) { pod in
                        NavigationLink(value: Route.pod(namespace: pod.namespace, name: pod.name)) {
                            PodRow(pod: pod, showsNamespace: false)
                        }
                    }
                }
            }
        }
    }
}
