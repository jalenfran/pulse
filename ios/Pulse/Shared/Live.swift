import PulseKit
import SwiftUI

/// Loads a value, keeps it fresh while the app is in the foreground, and
/// supports pull to refresh. Stale data stays on screen when a refresh fails;
/// a small glass capsule says so instead of replacing the content.
struct Live<Value: Sendable, Content: View>: View {
    var every: Duration = .seconds(15)
    let load: () async throws -> Value
    @ViewBuilder let content: (Value) -> Content

    @Environment(AppModel.self) private var app
    @Environment(\.scenePhase) private var scenePhase
    @State private var value: Value?
    @State private var error: (any Error)?

    var body: some View {
        Group {
            if let value {
                content(value)
            } else if let error {
                ContentUnavailableView {
                    Label("Can't Reach Pulse", systemImage: "wifi.exclamationmark")
                } description: {
                    Text(error.localizedDescription)
                } actions: {
                    Button("Try Again") { Task { await refresh() } }
                        .buttonStyle(.glass)
                }
            } else {
                ProgressView()
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .safeAreaInset(edge: .bottom) {
            if value != nil, error != nil {
                Label("Can't reach Pulse", systemImage: "wifi.exclamationmark")
                    .font(.footnote.weight(.medium))
                    .padding(.horizontal, 14)
                    .padding(.vertical, 8)
                    .glassEffect(.regular, in: .capsule)
                    .padding(.bottom, 8)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            }
        }
        .animation(.smooth, value: error == nil)
        .refreshable { await refresh() }
        .task(id: scenePhase) {
            guard scenePhase == .active else { return }
            while !Task.isCancelled {
                await refresh()
                try? await Task.sleep(for: every)
            }
        }
    }

    private func refresh() async {
        do {
            value = try await load()
            error = nil
        } catch {
            let cause = PulseError.underlying(error)
            if cause is CancellationError || (cause as? URLError)?.code == .cancelled { return }
            if (cause as? PulseError) == .unauthorized {
                app.tokenRejected()
                return
            }
            self.error = cause
        }
    }
}
