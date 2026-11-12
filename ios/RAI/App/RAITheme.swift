import Metal
import ProductCore
import SwiftUI
import SyncCore

enum RAIRenderer {
  // Simulator hosts can have no Metal GPU. Keep native material/readability
  // available there; physical iPhones retain the system Liquid Glass path.
  static let metalAvailable: Bool = {
    #if targetEnvironment(simulator)
      return MTLCreateSystemDefaultDevice() != nil
    #else
      return true
    #endif
  }()
}
enum RAITheme {
  static let background = Color(red: 0.97, green: 0.97, blue: 0.95)
  static let surface = Color.white
  static let surfaceMuted = Color(red: 0.93, green: 0.94, blue: 0.92)
  static let primary = Color(red: 0.08, green: 0.10, blue: 0.10)
  static let secondary = Color(red: 0.36, green: 0.39, blue: 0.38)
  static let accent = Color(red: 0.68, green: 0.89, blue: 0.90)
  static let lemon = Color(red: 0.97, green: 0.89, blue: 0.44)
  // Strong foreground colors stay legible on the warm, light surfaces.
  static let orange = Color(red: 0.12, green: 0.40, blue: 0.39)
  static let amber = Color(red: 0.50, green: 0.36, blue: 0.08)
  static let blue = Color(red: 0.12, green: 0.37, blue: 0.60)
  static let purple = Color(red: 0.47, green: 0.28, blue: 0.62)
  static let green = Color(red: 0.19, green: 0.44, blue: 0.26)
  static let charcoal = primary
  static let brown = surfaceMuted
  static let radius: CGFloat = 28
}
struct AmbientBackground: View {
  var body: some View {
    RAITheme.background.ignoresSafeArea().accessibilityHidden(true)
  }
}
struct RAIGlassSurface: ViewModifier {
  var radius: CGFloat = RAITheme.radius
  func body(content: Content) -> some View {
    content.background(RAITheme.surface, in: RoundedRectangle(cornerRadius: radius, style: .continuous))
  }
}
extension View {
  func raiGlass(radius: CGFloat = RAITheme.radius) -> some View {
    modifier(RAIGlassSurface(radius: radius))
  }
  func raiScreen() -> some View {
    frame(maxWidth: .infinity, maxHeight: .infinity).background { AmbientBackground() }
      .toolbarBackground(.hidden, for: .navigationBar)
      .toolbarColorScheme(.light, for: .navigationBar)
      .foregroundStyle(RAITheme.primary)
      .modifier(RAITabMaterialFallback())
  }
}
struct RAIPressStyle: ButtonStyle {
  @Environment(\.accessibilityReduceMotion) private var reduced
  func makeBody(configuration: Configuration) -> some View {
    configuration.label.scaleEffect(configuration.isPressed && !reduced ? 0.98 : 1).opacity(
      configuration.isPressed ? 0.88 : 1
    ).animation(reduced ? nil : .easeInOut(duration: 0.18), value: configuration.isPressed)
  }
}
struct CardHeading: View {
  let title: String, icon: String
  var body: some View {
    Label(title, systemImage: icon).font(.headline).foregroundStyle(RAITheme.primary)
  }
}
struct MetricText: View {
  let value: String, unit: String
  var body: some View {
    HStack(alignment: .firstTextBaseline, spacing: 4) {
      Text(value).font(.system(.largeTitle, design: .rounded, weight: .bold)).monospacedDigit()
      Text(unit).font(.subheadline).foregroundStyle(RAITheme.secondary)
    }.minimumScaleFactor(0.7)
  }
}
struct EmptyCard: View {
  let title: String, detail: String
  var body: some View {
    VStack(alignment: .leading, spacing: 10) {
      Text(title).font(.headline)
      Text(detail).font(.subheadline).foregroundStyle(RAITheme.secondary)
    }.frame(maxWidth: .infinity, alignment: .leading).padding(22).raiGlass()
  }
}
struct OrangeButton: View {
  let title: String, icon: String
  var action: () -> Void
  var body: some View {
    Button(action: action) {
      Label(title, systemImage: icon).font(.headline).frame(maxWidth: .infinity)
        .padding(.vertical, 16).foregroundStyle(RAITheme.primary)
        .background(RAITheme.lemon, in: Capsule())
    }.buttonStyle(RAIPressStyle()).tint(RAITheme.orange)
  }
}
/// Secondary action inside a card or under a list: a quiet tinted capsule, so
/// plain blue system text buttons do not float between cards.
struct RAISecondaryButtonStyle: ButtonStyle {
  @Environment(\.isEnabled) private var enabled
  func makeBody(configuration: Configuration) -> some View {
    configuration.label.font(.subheadline.weight(.semibold))
      .padding(.horizontal, 16).frame(minHeight: 40)
      .foregroundStyle(RAITheme.orange)
      .background(RAITheme.orange.opacity(0.10), in: Capsule())
      .opacity(enabled ? (configuration.isPressed ? 0.7 : 1) : 0.45)
  }
}
/// Action row inside a Form: tinted like a link, dimmed while disabled. The
/// screen-wide primary foreground would otherwise paint it as plain text.
struct RAIFormButtonStyle: ButtonStyle {
  @Environment(\.isEnabled) private var enabled
  func makeBody(configuration: Configuration) -> some View {
    configuration.label.foregroundStyle(RAITheme.orange)
      .frame(maxWidth: .infinity, alignment: .leading).contentShape(Rectangle())
      .opacity(enabled ? (configuration.isPressed ? 0.6 : 1) : 0.4)
  }
}
/// One-line navigation row drawn as a card, with a leading icon and chevron.
struct RAIRowLabel: View {
  let title: String, icon: String
  var detail: String?
  var body: some View {
    HStack(spacing: 12) {
      Image(systemName: icon).font(.body.weight(.semibold)).foregroundStyle(RAITheme.orange)
        .frame(width: 28)
      VStack(alignment: .leading, spacing: 2) {
        Text(title).font(.headline)
        if let detail {
          Text(detail).font(.caption).foregroundStyle(RAITheme.secondary)
            .multilineTextAlignment(.leading)
        }
      }
      Spacer(minLength: 0)
      Image(systemName: "chevron.right").font(.caption.weight(.semibold))
        .foregroundStyle(RAITheme.secondary)
    }.foregroundStyle(RAITheme.primary).padding(18).frame(maxWidth: .infinity, alignment: .leading)
      .raiGlass(radius: 20)
  }
}
/// Inline status message. Errors and hints share one shape instead of loose
/// coloured text at the bottom of a page.
struct RAINotice: View {
  enum Kind { case info, warning }
  let text: String
  var kind: Kind = .warning
  var body: some View {
    HStack(alignment: .top, spacing: 10) {
      Image(systemName: kind == .warning ? "exclamationmark.triangle.fill" : "info.circle.fill")
        .font(.subheadline).foregroundStyle(kind == .warning ? RAITheme.amber : RAITheme.blue)
        .accessibilityHidden(true)
      Text(text).font(.footnote).foregroundStyle(RAITheme.primary)
        .fixedSize(horizontal: false, vertical: true)
      Spacer(minLength: 0)
    }.padding(14).frame(maxWidth: .infinity, alignment: .leading)
      .background((kind == .warning ? RAITheme.lemon : RAITheme.accent).opacity(0.35),
                  in: RoundedRectangle(cornerRadius: 16, style: .continuous))
  }
}
/// Small capsule tag used for record states.
struct RAITag: View {
  let text: String
  var color: Color = RAITheme.secondary
  var body: some View {
    Text(text).font(.caption2.weight(.semibold)).padding(.horizontal, 8).padding(.vertical, 4)
      .background(color.opacity(0.16), in: Capsule()).foregroundStyle(color)
  }
}
struct HeroScene: View {
  var body: some View {
    RAITheme.accent.accessibilityHidden(true)
  }
}

struct RAITabMaterialFallback: ViewModifier {
  func body(content: Content) -> some View {
    if RAIRenderer.metalAvailable {
      content
    } else {
      content.toolbarBackground(.visible, for: .tabBar).toolbarBackground(
        RAITheme.surface, for: .tabBar)
    }
  }
}
struct RecordedBars: View {
  let values: [(String, Double?)]
  var color: Color = RAITheme.orange
  @ScaledMetric(relativeTo: .caption2) private var labelHeight = 18.0
  var body: some View {
    GeometryReader { geometry in
      let maximum = values.compactMap { $0.1 }.max() ?? 0
      let plotHeight = max(0, geometry.size.height - labelHeight)
      HStack(alignment: .bottom, spacing: 5) {
        ForEach(Array(values.enumerated()), id: \.offset) { _, entry in
          VStack(spacing: 4) {
            ZStack(alignment: .bottom) {
              Color.clear
              if let value = entry.1 {
                RoundedRectangle(cornerRadius: 3).fill(color.gradient)
                  .frame(
                    width: 12,
                    height: maximum > 0 ? plotHeight * CGFloat(max(0, value) / maximum) : 0)
              } else {
                Text("–").font(.caption2).foregroundStyle(RAITheme.secondary)
              }
            }.frame(height: plotHeight)
            Text(entry.0).font(.caption2).foregroundStyle(RAITheme.secondary).lineLimit(1)
              .minimumScaleFactor(0.6)
          }.frame(maxWidth: .infinity)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(entry.0 + "，" + ProductFormat.decimal(entry.1, digits: 1))
        }
      }
    }
  }
}
