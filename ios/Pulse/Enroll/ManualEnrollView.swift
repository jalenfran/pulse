import PulseKit
import SwiftUI

struct ManualEnrollView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    @State private var server = ""
    @State private var token = ""
    @State private var error: String?
    @FocusState private var focus: Field?

    private enum Field { case server, token }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField("pulse.example.com", text: $server)
                        .textContentType(.URL)
                        .keyboardType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .focused($focus, equals: .server)
                        .submitLabel(.next)
                        .onSubmit { focus = .token }
                } header: {
                    Text("Server")
                } footer: {
                    Text("The address of your Pulse reverse proxy. HTTPS is assumed.")
                }

                Section {
                    SecureField(text: $token) {
                        Text(verbatim: "pls_…")
                    }
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .focused($focus, equals: .token)
                    .submitLabel(.go)
                    .onSubmit(connect)
                } header: {
                    Text("Token")
                } footer: {
                    Text("Printed once by `pulse token create <name>`.")
                }

                Section {
                    PasteButton(payloadType: URL.self) { urls in
                        guard let url = urls.first else { return }
                        do {
                            let enrollment = try Enrollment(link: url)
                            server = enrollment.serverURL.absoluteString
                            token = enrollment.token
                            connect()
                        } catch {
                            self.error = error.localizedDescription
                        }
                    }
                    .labelStyle(.titleAndIcon)
                } footer: {
                    Text("Paste a copied `pulse://enroll` link to fill both fields.")
                }

                if let error {
                    Section {
                        Label(error, systemImage: "exclamationmark.triangle.fill")
                            .foregroundStyle(.orange)
                    }
                }
            }
            .navigationTitle("Connect")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel", systemImage: "xmark") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    if app.isEnrolling {
                        ProgressView()
                    } else {
                        Button("Connect", systemImage: "checkmark", action: connect)
                            .disabled(server.isEmpty || token.isEmpty)
                    }
                }
            }
            .onAppear { focus = .server }
        }
        .interactiveDismissDisabled(app.isEnrolling)
    }

    private func connect() {
        error = nil
        Task {
            do {
                try await app.enroll(Enrollment(server: server, token: token))
                dismiss()
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}
