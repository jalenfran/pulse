import PulseKit
import SwiftUI
import VisionKit

/// First run. The server prints a QR (`pulse token create <name>`); scanning
/// it here, or with the Camera app, opens `pulse://enroll?url=…&token=…`.
struct EnrollView: View {
    @Environment(AppModel.self) private var app
    @State private var isScanning = false
    @State private var isEnteringManually = false

    var body: some View {
        VStack(spacing: 0) {
            Spacer()
            VStack(spacing: 14) {
                Image(systemName: "waveform.path.ecg")
                    .font(.system(size: 52, weight: .semibold))
                    .foregroundStyle(.tint)
                    .padding(.bottom, 6)
                Text("Pulse")
                    .font(.largeTitle.bold())
                Text("Scan the code that `pulse token create` prints to connect this iPhone to your cluster.")
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .padding(.horizontal, 32)
            }
            Spacer()
            VStack(spacing: 12) {
                if DataScannerViewController.isSupported {
                    Button {
                        isScanning = true
                    } label: {
                        Label("Scan Code", systemImage: "qrcode.viewfinder")
                            .frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.glassProminent)
                }
                Button {
                    isEnteringManually = true
                } label: {
                    Text("Enter Manually")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.glass)
            }
            .controlSize(.extraLarge)
            .padding(.horizontal, 24)
            .disabled(app.isEnrolling)
        }
        .padding(.bottom, 16)
        .overlay {
            if app.isEnrolling {
                ProgressView("Connecting…")
                    .padding(24)
                    .glassEffect(.regular, in: .rect(cornerRadius: 24))
            }
        }
        .sheet(isPresented: $isEnteringManually) {
            ManualEnrollView()
        }
        .fullScreenCover(isPresented: $isScanning) {
            ScanView { url in
                isScanning = false
                app.open(url)
            }
        }
    }
}

private struct ScanView: View {
    let found: (URL) -> Void
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            QRScanner(found: found)
                .ignoresSafeArea()
                .navigationTitle("Scan Code")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Cancel", systemImage: "xmark") { dismiss() }
                    }
                }
                .safeAreaInset(edge: .bottom) {
                    Text("Point at the code in your terminal.")
                        .font(.subheadline.weight(.medium))
                        .padding(.horizontal, 16)
                        .padding(.vertical, 10)
                        .glassEffect(.regular, in: .capsule)
                        .padding(.bottom, 12)
                }
        }
    }
}

private struct QRScanner: UIViewControllerRepresentable {
    let found: (URL) -> Void

    func makeUIViewController(context: Context) -> DataScannerViewController {
        let scanner = DataScannerViewController(
            recognizedDataTypes: [.barcode(symbologies: [.qr])],
            qualityLevel: .balanced,
            recognizesMultipleItems: false,
            isHighFrameRateTrackingEnabled: false,
            isHighlightingEnabled: true
        )
        scanner.delegate = context.coordinator
        return scanner
    }

    func updateUIViewController(_ scanner: DataScannerViewController, context: Context) {
        if !scanner.isScanning { try? scanner.startScanning() }
    }

    static func dismantleUIViewController(_ scanner: DataScannerViewController, coordinator: Coordinator) {
        scanner.stopScanning()
    }

    func makeCoordinator() -> Coordinator { Coordinator(found: found) }

    final class Coordinator: NSObject, DataScannerViewControllerDelegate {
        let found: (URL) -> Void
        private var handled = false

        init(found: @escaping (URL) -> Void) { self.found = found }

        func dataScanner(
            _ dataScanner: DataScannerViewController, didAdd addedItems: [RecognizedItem], allItems: [RecognizedItem]
        ) {
            for case .barcode(let code) in addedItems {
                guard !handled, let payload = code.payloadStringValue, let url = URL(string: payload),
                      url.scheme?.lowercased() == "pulse"
                else { continue }
                handled = true
                UINotificationFeedbackGenerator().notificationOccurred(.success)
                found(url)
            }
        }
    }
}
