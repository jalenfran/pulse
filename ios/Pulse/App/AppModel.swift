import Observation
import PulseKit
import SwiftUI

enum AppTab: Hashable {
    case overview, nodes, workloads, events
}

/// Who we're connected to, and app-wide navigation state.
@Observable
final class AppModel {
    private(set) var client: PulseClient?
    var tab: AppTab = .overview
    /// Set when an opened `pulse://` link can't be used.
    var linkError: String?
    private(set) var isEnrolling = false

    init() {
        if let enrollment = CredentialStore.load() {
            client = PulseClient(enrollment: enrollment)
        }
        #if DEBUG
        // Simulator runs: `SIMCTL_CHILD_PULSE_ENROLL='pulse://enroll?…' xcrun simctl launch …`
        // enrolls without the system's "Open in Pulse?" prompt.
        if let link = ProcessInfo.processInfo.environment["PULSE_ENROLL"].flatMap(URL.init(string:)) {
            open(link)
        }
        switch ProcessInfo.processInfo.environment["PULSE_TAB"] {
        case "nodes": tab = .nodes
        case "workloads": tab = .workloads
        case "events": tab = .events
        default: break
        }
        #endif
    }

    /// Checks the server accepts the token before saving anything, so a typo
    /// or a host token never leaves the app in a broken signed-in state.
    func enroll(_ enrollment: Enrollment) async throws {
        isEnrolling = true
        defer { isEnrolling = false }
        let candidate = PulseClient(enrollment: enrollment)
        do {
            _ = try await candidate.overview()
        } catch {
            throw PulseError.underlying(error)
        }
        try CredentialStore.save(enrollment)
        tab = .overview
        client = candidate
    }

    func signOut() {
        CredentialStore.delete()
        client = nil
    }

    /// The token was revoked or replaced on the server.
    func tokenRejected() {
        signOut()
        linkError = PulseError.unauthorized.localizedDescription
    }

    /// `pulse://enroll?…`, from the system Camera app or a pasted link.
    func open(_ url: URL) {
        Task {
            do {
                try await enroll(Enrollment(link: url))
            } catch {
                linkError = error.localizedDescription
            }
        }
    }
}

extension Optional {
    /// Lets an optional drive `alert(isPresented:)`.
    var isPresent: Bool {
        get { self != nil }
        set { if !newValue { self = nil } }
    }
}

extension EnvironmentValues {
    /// Placeholder until `RootView` injects the enrolled client.
    @Entry var pulse = PulseClient(
        enrollment: Enrollment(serverURL: URL(string: "https://pulse.invalid")!, token: "pls_preview")
    )
}
