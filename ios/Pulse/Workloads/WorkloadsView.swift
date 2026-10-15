import PulseKit
import SwiftUI

struct WorkloadsView: View {
    @Environment(\.pulse) private var pulse
    @State private var query = ""
    @State private var filter = WorkloadFilter.all

    var body: some View {
        NavigationStack {
            Live(load: { try await pulse.workloads() }) { workloads in
                WorkloadList(workloads: workloads, query: query, filter: $filter)
            }
            .navigationTitle("Workloads")
            .searchable(text: $query, prompt: "Name, namespace or image")
            .pulseDestinations()
        }
    }
}

enum WorkloadFilter: Hashable {
    case all, problems
}

private struct WorkloadList: View {
    let workloads: [Workload]
    let query: String
    @Binding var filter: WorkloadFilter

    private var unhealthyCount: Int { workloads.count(where: { !$0.healthy }) }

    private var shown: [Workload] {
        let q = query.trimmingCharacters(in: .whitespaces)
        return workloads.filter { w in
            (filter == .all || !w.healthy)
                && (q.isEmpty
                    || w.name.localizedCaseInsensitiveContains(q)
                    || w.namespace.localizedCaseInsensitiveContains(q)
                    || w.images.contains { $0.localizedCaseInsensitiveContains(q) })
        }
    }

    /// Namespaces alphabetically; inside each, problems first.
    private var groups: [(namespace: String, items: [Workload])] {
        Dictionary(grouping: shown, by: \.namespace)
            .map { ns, items in
                (ns, items.sorted { ($0.healthy ? 1 : 0, $0.name) < ($1.healthy ? 1 : 0, $1.name) })
            }
            .sorted { $0.namespace < $1.namespace }
    }

    var body: some View {
        List {
            Section {
                Picker("Show", selection: $filter) {
                    Text("All · \(workloads.count)").tag(WorkloadFilter.all)
                    Text("Problems · \(unhealthyCount)").tag(WorkloadFilter.problems)
                }
                .pickerStyle(.segmented)
                .listRowInsets(EdgeInsets())
                .listRowBackground(Color.clear)
            }

            ForEach(groups, id: \.namespace) { group in
                Section(group.namespace) {
                    ForEach(group.items) { workload in
                        NavigationLink(
                            value: Route.workload(namespace: workload.namespace, name: workload.name, kind: workload.kind)
                        ) {
                            WorkloadRow(workload: workload)
                        }
                    }
                }
            }
        }
        .listSectionSpacing(.compact)
        .overlay {
            if shown.isEmpty {
                if !query.isEmpty {
                    ContentUnavailableView.search(text: query)
                } else if filter == .problems {
                    ContentUnavailableView(
                        "No Problems",
                        systemImage: "checkmark.circle",
                        description: Text("All \(workloads.count) workloads are healthy.")
                    )
                }
            }
        }
        .animation(.smooth, value: filter)
    }
}

struct WorkloadRow: View {
    let workload: Workload

    private var subtitle: String {
        if workload.kind == .cronJob {
            return [workload.kind.rawValue, workload.schedule].compactMap(\.self).joined(separator: " · ")
        }
        guard workload.restarts > 0 else { return workload.kind.rawValue }
        return "\(workload.kind.rawValue) · \(Units.restarts(workload.restarts))"
    }

    var body: some View {
        HStack(spacing: 8) {
            VStack(alignment: .leading, spacing: 1) {
                Text(workload.name)
                Text(subtitle)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }
            Spacer()
            if !workload.healthy {
                Image(systemName: "exclamationmark.triangle.fill")
                    .foregroundStyle(.orange)
                    .imageScale(.small)
                    .accessibilityLabel("Unhealthy")
            }
            Text(workload.status)
                .font(.subheadline)
                .monospacedDigit()
                .foregroundStyle(workload.healthy ? Color.secondary : .orange)
        }
        .padding(.vertical, 2)
    }
}
