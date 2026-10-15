import PulseKit
import SwiftUI

struct EventsView: View {
    private enum SourceFilter: Hashable {
        case all, kubernetes, hosts

        var query: String? {
            switch self {
            case .all: nil
            case .kubernetes: "k8s"
            case .hosts: "host"
            }
        }
    }

    private struct Filter: Hashable {
        var minSeverity: Severity?
        var source: SourceFilter
    }

    @Environment(\.pulse) private var pulse
    @Environment(AppModel.self) private var app
    @Environment(\.scenePhase) private var scenePhase
    @State private var filter = Filter(minSeverity: nil, source: .all)
    @State private var events: [Event] = []
    @State private var cursor: String?
    @State private var loaded = false
    @State private var error: (any Error)?

    var body: some View {
        @Bindable var app = app
        NavigationStack(path: $app.eventsPath) {
            content
                .navigationTitle("Events")
                .toolbar {
                    ToolbarItem(placement: .topBarTrailing) { filterMenu }
                }
                .refreshable { await reload() }
                .task(id: filter) { await reload() }
                .task(id: scenePhase) {
                    // New events arrive at the top: check on returning to the
                    // foreground, then every 30s while visible. The server
                    // answers from cached state, so faster gains nothing.
                    guard scenePhase == .active else { return }
                    while !Task.isCancelled {
                        await fetchNewer()
                        try? await Task.sleep(for: .seconds(30))
                    }
                }
                .pulseDestinations()
        }
    }

    @ViewBuilder private var content: some View {
        if !loaded {
            if let error {
                ContentUnavailableView {
                    Label("Can't Reach Pulse", systemImage: "wifi.exclamationmark")
                } description: {
                    Text(error.localizedDescription)
                } actions: {
                    Button("Try Again") { Task { await reload() } }
                        .buttonStyle(.glass)
                }
            } else {
                ProgressView()
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        } else if events.isEmpty {
            ContentUnavailableView(
                "No Events",
                systemImage: "list.bullet",
                description: Text(isFiltered ? "Nothing matches these filters." : "Events from Kubernetes and your hosts appear here.")
            )
        } else {
            List {
                ForEach(days, id: \.day) { group in
                    Section(Self.dayTitle(group.day)) {
                        ForEach(group.events) { event in
                            NavigationLink(value: Route.event(event)) {
                                EventRow(event: event, showsTime: true)
                            }
                        }
                    }
                }
                if cursor != nil {
                    ProgressView()
                        .frame(maxWidth: .infinity)
                        .listRowBackground(Color.clear)
                        .task { await loadMore() }
                }
            }
        }
    }

    private var filterMenu: some View {
        Menu {
            Picker("Severity", selection: $filter.minSeverity) {
                Text("All Severities").tag(Severity?.none)
                Label("Warnings and Critical", systemImage: Severity.warning.symbol).tag(Severity?.some(.warning))
                Label("Critical Only", systemImage: Severity.critical.symbol).tag(Severity?.some(.critical))
            }
            .pickerStyle(.inline)
            Picker("Source", selection: $filter.source) {
                Text("All Sources").tag(SourceFilter.all)
                Label("Kubernetes", systemImage: "shippingbox").tag(SourceFilter.kubernetes)
                Label("Hosts", systemImage: "server.rack").tag(SourceFilter.hosts)
            }
            .pickerStyle(.inline)
        } label: {
            Label(
                "Filter",
                systemImage: isFiltered ? "line.3.horizontal.decrease.circle.fill" : "line.3.horizontal.decrease"
            )
        }
    }

    private var isFiltered: Bool { filter.minSeverity != nil || filter.source != .all }

    private var days: [(day: Date, events: [Event])] {
        Dictionary(grouping: events) { Calendar.current.startOfDay(for: $0.ts) }
            .map { ($0.key, $0.value) }
            .sorted { $0.day > $1.day }
    }

    private static func dayTitle(_ day: Date) -> String {
        if Calendar.current.isDateInToday(day) { return String(localized: "Today") }
        if Calendar.current.isDateInYesterday(day) { return String(localized: "Yesterday") }
        return day.formatted(.dateTime.weekday(.wide).month(.abbreviated).day())
    }

    private func page(before: String? = nil) async throws -> PulseClient.EventPage {
        try await pulse.events(before: before, minSeverity: filter.minSeverity, source: filter.source.query)
    }

    private func reload() async {
        do {
            let first = try await page()
            events = first.items
            cursor = first.nextCursor
            error = nil
            loaded = true
        } catch {
            handle(error)
        }
    }

    private func loadMore() async {
        guard let cursor else { return }
        do {
            let next = try await page(before: cursor)
            let known = Set(events.map(\.id))
            events += next.items.filter { !known.contains($0.id) }
            self.cursor = next.nextCursor
        } catch {
            handle(error)
        }
    }

    /// IDs are ULIDs, so anything newer than the top row sorts above it.
    private func fetchNewer() async {
        guard loaded, let newest = events.first?.id else { return }
        guard let first = try? await page() else { return }
        let newer = first.items.filter { $0.id > newest }
        if !newer.isEmpty {
            withAnimation(.smooth) { events.insert(contentsOf: newer, at: 0) }
        }
    }

    private func handle(_ error: any Error) {
        let cause = PulseError.underlying(error)
        if cause is CancellationError || (cause as? URLError)?.code == .cancelled { return }
        if (cause as? PulseError) == .unauthorized {
            app.tokenRejected()
            return
        }
        self.error = cause
    }
}

struct EventRow: View {
    let event: Event
    /// Clock time instead of "2m"; the Events tab already groups by day.
    var showsTime = false

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 12) {
            Image(systemName: event.symbol)
                .foregroundStyle(event.severity.tint)
                .frame(width: 22)
            VStack(alignment: .leading, spacing: 2) {
                Text(event.title)
                    .lineLimit(3)
                Text(event.sourceLabel)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }
            Spacer(minLength: 8)
            Group {
                if showsTime {
                    Text(event.ts, format: .dateTime.hour().minute())
                } else {
                    Text(event.ts.shortRelative)
                }
            }
            .font(.footnote)
            .foregroundStyle(.secondary)
            .monospacedDigit()
        }
        .padding(.vertical, 2)
        .accessibilityElement(children: .combine)
    }
}

struct EventDetailView: View {
    let event: Event

    var body: some View {
        List {
            Section {
                HStack(alignment: .firstTextBaseline, spacing: 12) {
                    Image(systemName: event.symbol)
                        .foregroundStyle(event.severity.tint)
                        .font(.title3)
                    Text(event.title)
                        .font(.headline)
                }
                .padding(.vertical, 4)
            }

            Section {
                LabeledContent("Severity") {
                    Text(event.severity.label)
                        .foregroundStyle(event.severity == .info ? Color.secondary : event.severity.tint)
                }
                LabeledContent("Source", value: event.sourceLabel)
                LabeledContent("Kind") {
                    Text(event.kind).monospaced()
                }
                LabeledContent("Time") {
                    Text(event.ts, format: .dateTime.month().day().hour().minute().second())
                }
            }

            if let body = event.body, !body.isEmpty {
                Section("Details") {
                    Text(body)
                        .font(.footnote.monospaced())
                        .textSelection(.enabled)
                }
            }

            if let host = event.hostName {
                Section {
                    NavigationLink(value: Route.node(host)) {
                        Label("Show \(host)", systemImage: "server.rack")
                    }
                }
            }
        }
        .navigationTitle("Event")
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// An event opened from a notification, known only by its ID.
struct EventLinkView: View {
    let id: String
    @Environment(\.pulse) private var pulse

    var body: some View {
        Live(every: nil, load: { try await pulse.event(id: id) }) { event in
            EventDetailView(event: event)
        }
        .navigationTitle("Event")
        .navigationBarTitleDisplayMode(.inline)
    }
}
