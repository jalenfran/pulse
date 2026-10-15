import PulseKit
import SwiftUI

struct OverviewView: View {
    @Environment(\.pulse) private var pulse
    @Environment(AppModel.self) private var app
    @State private var confirmingSignOut = false

    var body: some View {
        NavigationStack {
            Live(load: pulse.overview) { overview in
                OverviewList(overview: overview)
            }
            .navigationTitle("Pulse")
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Menu {
                        Section(pulse.enrollment.serverURL.host() ?? "") {
                            Button("Sign Out", systemImage: "rectangle.portrait.and.arrow.right", role: .destructive) {
                                confirmingSignOut = true
                            }
                        }
                    } label: {
                        Label("More", systemImage: "ellipsis")
                    }
                }
            }
            .confirmationDialog(
                "Sign out of \(pulse.enrollment.serverURL.host() ?? "Pulse")?",
                isPresented: $confirmingSignOut,
                titleVisibility: .visible
            ) {
                Button("Sign Out", role: .destructive) { app.signOut() }
            } message: {
                Text("The token is removed from this iPhone. You'll need a new one to reconnect.")
            }
            .pulseDestinations()
        }
    }
}

private struct OverviewList: View {
    let overview: Overview
    @Environment(AppModel.self) private var app

    private var openProblems: [Problem] { overview.problems.filter { $0.severity > .info } }

    var body: some View {
        List {
            Section {
                SummaryRow(overview: overview, problemCount: openProblems.count)
                ForEach(overview.problems) { problem in
                    NavigationLink(value: problem.route) {
                        ProblemRow(problem: problem)
                    }
                }
            }

            Section {
                ForEach(overview.nodes) { node in
                    NavigationLink(value: Route.node(node.name)) {
                        NodeRingRow(node: node)
                    }
                }
            } header: {
                HeaderWithAction(title: "Nodes", action: "Show All") { app.tab = .nodes }
            } footer: {
                Text("CPU and memory, percent of allocatable.")
            }

            if !overview.hosts.isEmpty {
                Section("Hosts") {
                    ForEach(overview.hosts) { host in
                        HostRow(host: host)
                    }
                }
            }

            if !overview.recentEvents.isEmpty {
                Section {
                    ForEach(overview.recentEvents.prefix(5)) { event in
                        NavigationLink(value: Route.event(event)) {
                            EventRow(event: event)
                        }
                    }
                } header: {
                    HeaderWithAction(title: "Recent", action: "Show All") { app.tab = .events }
                }
            }
        }
        .headerProminence(.increased)
        .navigationSubtitle("Updated \(overview.generatedAt.formatted(.relative(presentation: .named)))")
    }
}

private struct SummaryRow: View {
    let overview: Overview
    let problemCount: Int

    private var worst: Severity? { overview.problems.map(\.severity).filter { $0 > .info }.max() }
    private var readyNodes: Int { overview.nodes.count(where: \.ready) }

    var body: some View {
        HStack(spacing: 14) {
            Image(systemName: worst?.symbol ?? "checkmark.circle.fill")
                .font(.system(size: 30))
                .foregroundStyle(worst?.tint ?? .secondary)
                .contentTransition(.symbolEffect(.replace))
            VStack(alignment: .leading, spacing: 2) {
                Group {
                    if problemCount == 0 {
                        Text("All Systems Normal")
                    } else {
                        Text("^[\(problemCount) Problem](inflect: true)")
                    }
                }
                .font(.title2.bold())
                Text("\(readyNodes) of \(overview.nodes.count) nodes ready · \(overview.workloads.healthy) of \(overview.workloads.total) workloads")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 6)
        .accessibilityElement(children: .combine)
    }
}

private struct ProblemRow: View {
    let problem: Problem

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(problem.title)
            if let detail = problem.detail, !detail.isEmpty {
                Text(detail)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
            }
        }
        .padding(.leading, 44)
        .padding(.vertical, 2)
    }
}

struct NodeRingRow: View {
    let node: NodeSummary

    var body: some View {
        HStack(spacing: 12) {
            StatusSymbol(problem: node.isHealthy ? nil : (node.ready ? .warning : .critical))
            VStack(alignment: .leading, spacing: 1) {
                Text(node.name)
                Text("\(node.roleLabel) · \(node.podCount) pods")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }
            Spacer()
            UsageRing(label: "CPU", usage: node.cpu)
            UsageRing(label: "Memory", usage: node.memory)
        }
        .padding(.vertical, 2)
    }
}

private struct HostRow: View {
    let host: HostStatus

    private var health: Severity? {
        guard let severity = host.lastHealth?.severity, severity > .info else { return nil }
        return severity
    }

    private var detail: String {
        var parts: [String] = []
        if let upgrade = host.lastUpgrade { parts.append(String(localized: "Upgraded \(upgrade.ts.shortRelative)")) }
        if let boot = host.lastBoot { parts.append(String(localized: "Booted \(boot.ts.shortRelative)")) }
        return parts.isEmpty ? String(localized: "No reports yet") : parts.joined(separator: " · ")
    }

    var body: some View {
        HStack(spacing: 12) {
            StatusSymbol(problem: health)
            VStack(alignment: .leading, spacing: 1) {
                Text(host.host)
                Text(detail)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 2)
    }
}

extension NodeSummary {
    /// k3s labels its server nodes both control-plane and master; one is enough.
    var roleLabel: String {
        if roles.contains("control-plane") { return "control-plane" }
        return roles.first ?? String(localized: "worker")
    }
}
