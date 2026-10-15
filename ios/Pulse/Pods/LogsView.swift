import PulseKit
import SwiftUI

struct LogsView: View {
    let namespace: String
    let pod: String
    let container: String
    /// The container has restarted, so the crashed instance's log exists.
    let hasPrevious: Bool

    private enum Instance: Hashable { case current, previous }

    private struct Load: Hashable {
        var instance: Instance
        var following: Bool
    }

    @Environment(\.pulse) private var pulse
    @Environment(AppModel.self) private var app
    @AppStorage("logs.wrap") private var wrap = true
    @State private var instance = Instance.current
    @State private var following = true
    @State private var lines: [LogLine] = []
    @State private var nextID = 0
    @State private var loading = true
    @State private var ended = false
    @State private var error: (any Error)?

    /// Enough to scroll back through a crash without growing forever.
    private let maxLines = 5000

    var body: some View {
        content
            .navigationTitle(container)
            .navigationSubtitle(pod)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar(.hidden, for: .tabBar)
            .safeAreaInset(edge: .top) {
                if hasPrevious {
                    Picker("Instance", selection: $instance) {
                        Text("Current").tag(Instance.current)
                        Text("Previous").tag(Instance.previous)
                    }
                    .pickerStyle(.segmented)
                    .padding(.horizontal)
                    .padding(.bottom, 8)
                }
            }
            .toolbar {
                ToolbarItem(placement: .bottomBar) {
                    Button {
                        following.toggle()
                    } label: {
                        HStack(spacing: 8) {
                            Circle()
                                .fill(isLive ? Color.accentColor : .secondary)
                                .frame(width: 8, height: 8)
                            Text(isLive ? "Live" : "Paused")
                        }
                        .fontWeight(.semibold)
                    }
                    .disabled(instance == .previous)
                    .accessibilityLabel(isLive ? "Pause following" : "Follow new lines")
                }
                ToolbarSpacer(.flexible, placement: .bottomBar)
                ToolbarItemGroup(placement: .bottomBar) {
                    Button(wrap ? "Don't Wrap Lines" : "Wrap Lines", systemImage: "text.word.spacing") {
                        wrap.toggle()
                    }
                    ShareLink(item: shareText, preview: SharePreview("\(pod) \(container) log"))
                        .disabled(lines.isEmpty)
                }
            }
            .task(id: Load(instance: instance, following: following)) {
                await load()
            }
    }

    private var isLive: Bool { following && instance == .current && !ended }

    @ViewBuilder private var content: some View {
        if lines.isEmpty {
            if loading {
                ProgressView()
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if let error {
                ContentUnavailableView(
                    "Couldn't Load Logs",
                    systemImage: "exclamationmark.triangle",
                    description: Text(error.localizedDescription)
                )
            } else {
                ContentUnavailableView(
                    "No Output",
                    systemImage: "text.alignleft",
                    description: Text(instance == .previous ? "The previous instance logged nothing." : "Nothing logged yet.")
                )
            }
        } else {
            ScrollView(wrap ? .vertical : [.vertical, .horizontal]) {
                LazyVStack(alignment: .leading, spacing: 3) {
                    ForEach(lines) { line in
                        LogLineView(line: line, wrap: wrap)
                    }
                    if ended || instance == .previous {
                        Text(instance == .previous ? "End of previous instance" : "Container exited")
                            .font(.footnote)
                            .foregroundStyle(.secondary)
                            .padding(.top, 8)
                    }
                }
                .padding(.horizontal)
                .padding(.vertical, 8)
                .textSelection(.enabled)
            }
            .defaultScrollAnchor(.bottom)
            .defaultScrollAnchor(.bottom, for: .sizeChanges)
        }
    }

    private func load() async {
        switch (instance, following) {
        case (.current, false):
            // Paused: keep what's on screen.
            return
        case (.current, true):
            reset()
            do {
                for try await event in pulse.followLogs(namespace: namespace, pod: pod, container: container, tail: 500) {
                    loading = false
                    switch event {
                    case .line(let raw): append(raw)
                    case .ended: ended = true
                    }
                }
            } catch {
                fail(error)
            }
            loading = false
        case (.previous, _):
            reset()
            do {
                lines = try await pulse.logs(
                    namespace: namespace, pod: pod, container: container, previous: true, tail: maxLines
                )
            } catch {
                fail(error)
            }
            loading = false
        }
    }

    private func reset() {
        lines = []
        loading = true
        ended = false
        error = nil
    }

    private func append(_ raw: String) {
        lines.append(LogLine(id: nextID, raw: raw))
        nextID += 1
        if lines.count > maxLines { lines.removeFirst(lines.count - maxLines) }
    }

    private func fail(_ error: any Error) {
        let cause = PulseError.underlying(error)
        if cause is CancellationError || (cause as? URLError)?.code == .cancelled { return }
        if (cause as? PulseError) == .unauthorized {
            app.tokenRejected()
            return
        }
        self.error = cause
        following = false
    }

    private var shareText: String {
        lines.map { line in
            line.timestamp.map { "\($0.formatted(.iso8601)) \(line.text)" } ?? line.text
        }
        .joined(separator: "\n")
    }
}

private struct LogLineView: View {
    let line: LogLine
    let wrap: Bool

    private static let time = Date.VerbatimFormatStyle(
        format: "\(hour: .twoDigits(clock: .twentyFourHour, hourCycle: .zeroBased)):\(minute: .twoDigits):\(second: .twoDigits)",
        timeZone: .current,
        calendar: .current
    )

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            if let timestamp = line.timestamp {
                Text(timestamp, format: Self.time)
                    .foregroundStyle(.tertiary)
            }
            Text(line.text)
                .foregroundStyle(color)
                .lineLimit(wrap ? nil : 1)
                .fixedSize(horizontal: !wrap, vertical: false)
        }
        .font(.caption.monospaced())
    }

    private var color: Color {
        switch line.level {
        case .error: .red
        case .warning: .orange
        case nil: .primary
        }
    }
}
