import PulseKit
import SwiftUI

@main
struct PulseApp: App {
    @State private var app = AppModel()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(app)
                .preferredColorScheme(.dark)
                .onOpenURL { url in app.open(url) }
        }
    }
}

struct RootView: View {
    @Environment(AppModel.self) private var app

    var body: some View {
        @Bindable var app = app
        Group {
            if let client = app.client {
                MainTabView()
                    .environment(\.pulse, client)
                    // A new enrollment means a new server: rebuild every screen.
                    .id(client.enrollment)
            } else {
                EnrollView()
            }
        }
        .alert("Couldn't Connect", isPresented: $app.linkError.isPresent, presenting: app.linkError) { _ in
            Button("OK") {}
        } message: { message in
            Text(message)
        }
    }
}

struct MainTabView: View {
    @Environment(AppModel.self) private var app

    var body: some View {
        @Bindable var app = app
        TabView(selection: $app.tab) {
            Tab("Overview", systemImage: "waveform.path.ecg", value: AppTab.overview) {
                OverviewView()
            }
            Tab("Nodes", systemImage: "server.rack", value: AppTab.nodes) {
                NodesView()
            }
            Tab("Workloads", systemImage: "square.stack.3d.up", value: AppTab.workloads) {
                WorkloadsView()
            }
            Tab("Events", systemImage: "list.bullet", value: AppTab.events) {
                EventsView()
            }
        }
        .tabBarMinimizeBehavior(.onScrollDown)
    }
}
