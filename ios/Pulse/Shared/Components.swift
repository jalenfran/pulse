import PulseKit
import SwiftUI

/// Leading status symbol for a row: a gray check when fine, the severity's
/// symbol and color when not.
struct StatusSymbol: View {
    var problem: Severity?

    var body: some View {
        Image(systemName: problem?.symbol ?? "checkmark.circle.fill")
            .foregroundStyle(problem?.tint ?? .secondary)
            .font(.title3)
            .accessibilityLabel(problem?.label ?? String(localized: "Healthy"))
    }
}

/// Small ring with the percentage inside, like an accessory capacity gauge
/// but sized for a list row.
struct UsageRing: View {
    let label: String
    let usage: Usage?

    var body: some View {
        ZStack {
            Circle()
                .stroke(.quaternary, lineWidth: 3.5)
            if let usage {
                Circle()
                    .trim(from: 0, to: usage.fraction)
                    .stroke(usage.tint, style: StrokeStyle(lineWidth: 3.5, lineCap: .round))
                    .rotationEffect(.degrees(-90))
                Text(usage.percent, format: .number.precision(.fractionLength(0)))
                    .font(.system(size: 10, weight: .semibold, design: .rounded))
                    .monospacedDigit()
            } else {
                Text(verbatim: "–")
                    .font(.system(size: 10, weight: .semibold, design: .rounded))
                    .foregroundStyle(.secondary)
            }
        }
        .frame(width: 30, height: 30)
        .animation(.smooth, value: usage?.percent)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(label)
        .accessibilityValue(usage.map { "\(Int($0.percent)) percent" } ?? String(localized: "No metrics"))
    }
}

/// Labelled capacity bar: "CPU  1.5 of 4".
struct UsageBar: View {
    let label: String
    let usage: Usage?
    let format: (Usage) -> String

    var body: some View {
        VStack(alignment: .leading, spacing: 7) {
            HStack(spacing: 6) {
                Text(label).foregroundStyle(.secondary)
                Spacer(minLength: 0)
                if let usage {
                    Text(format(usage))
                        .foregroundStyle(usage.percent >= 80 ? usage.tint : .primary)
                        .monospacedDigit()
                } else {
                    Text("No metrics").foregroundStyle(.secondary)
                }
            }
            .font(.footnote)
            .lineLimit(1)
            .minimumScaleFactor(0.8)
            Capsule()
                .fill(.quaternary)
                .frame(height: 5)
                .overlay(alignment: .leading) {
                    if let usage {
                        GeometryReader { proxy in
                            Capsule()
                                .fill(usage.tint)
                                .frame(width: max(5, proxy.size.width * usage.fraction))
                        }
                    }
                }
                .animation(.smooth, value: usage?.percent)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(label)
        .accessibilityValue(usage.map { "\(format($0)), \(Int($0.percent)) percent" } ?? String(localized: "No metrics"))
    }
}

/// Section header with a trailing action, in the increased-prominence style.
struct HeaderWithAction: View {
    let title: LocalizedStringKey
    let action: LocalizedStringKey
    let perform: () -> Void

    var body: some View {
        HStack(alignment: .firstTextBaseline) {
            Text(title)
            Spacer()
            Button(action, action: perform)
                .font(.body)
                .textCase(nil)
        }
    }
}
