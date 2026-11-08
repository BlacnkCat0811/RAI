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
  static let orange = Color(red: 1, green: 0.48, blue: 0.10)
  static let amber = Color(red: 1, green: 0.70, blue: 0.36)
  static let charcoal = Color(red: 0.086, green: 0.078, blue: 0.071)
  static let brown = Color(red: 0.17, green: 0.13, blue: 0.105)
  static let blue = Color(red: 0.34, green: 0.62, blue: 1)
  static let purple = Color(red: 0.64, green: 0.46, blue: 1)
  static let green = Color(red: 0.35, green: 0.88, blue: 0.63)
  static let secondary = Color.white.opacity(0.66)
  static let radius: CGFloat = 28
}
struct AmbientBackground: View {
  var body: some View {
    GeometryReader { g in
      ZStack {
        RAITheme.charcoal
        LinearGradient(
          colors: [Color(red: 0.10, green: 0.13, blue: 0.20), RAITheme.brown, RAITheme.charcoal],
          startPoint: .topTrailing, endPoint: .bottomLeading)
        RadialGradient(
          colors: [RAITheme.orange.opacity(0.30), .clear], center: UnitPoint(x: 0.02, y: 0.30),
          startRadius: 1, endRadius: g.size.width * 0.8)
        RadialGradient(
          colors: [RAITheme.amber.opacity(0.18), .clear], center: UnitPoint(x: 1, y: 0.75),
          startRadius: 1, endRadius: g.size.width * 0.75)
      }
    }.ignoresSafeArea().accessibilityHidden(true)
  }
}
struct RAIGlassSurface: ViewModifier {
  @Environment(\.accessibilityReduceTransparency) private var opaque
  var radius: CGFloat = RAITheme.radius
  func body(content: Content) -> some View {
    let shape = RoundedRectangle(cornerRadius: radius, style: .continuous)
    if opaque {
      content.background(RAITheme.brown, in: shape).overlay(
        shape.strokeBorder(.white.opacity(0.25), lineWidth: 0.5))
    } else if #available(iOS 26.0, *), RAIRenderer.metalAvailable {
      content.glassEffect(.regular.tint(RAITheme.brown.opacity(0.20)), in: shape)
    } else {
      content.background(.ultraThinMaterial, in: shape).background(
        RAITheme.brown.opacity(0.55), in: shape
      ).overlay(
        shape.strokeBorder(
          LinearGradient(
            colors: [.white.opacity(0.30), .white.opacity(0.07)], startPoint: .topLeading,
            endPoint: .bottomTrailing), lineWidth: 0.5)
      ).shadow(color: .black.opacity(0.16), radius: 8, y: 4)
    }
  }
}
extension View {
  func raiGlass(radius: CGFloat = RAITheme.radius) -> some View {
    modifier(RAIGlassSurface(radius: radius))
  }
  func raiScreen() -> some View {
    frame(maxWidth: .infinity, maxHeight: .infinity).background { AmbientBackground() }
      .toolbarBackground(.hidden, for: .navigationBar)
      .toolbarColorScheme(.dark, for: .navigationBar)
      .foregroundStyle(.white)
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
    Label(title, systemImage: icon).font(.headline).foregroundStyle(.white.opacity(0.90))
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
      Label(title, systemImage: icon).font(.headline).frame(maxWidth: .infinity).padding(
        .vertical, 16
      ).background(
        LinearGradient(
          colors: [RAITheme.amber.opacity(0.40), RAITheme.orange.opacity(0.20)],
          startPoint: .topLeading, endPoint: .bottomTrailing), in: Capsule()
      ).overlay(Capsule().strokeBorder(.white.opacity(0.40), lineWidth: 0.5))
    }.buttonStyle(RAIPressStyle()).tint(RAITheme.orange)
  }
}
@MainActor private enum HeroArtwork {
  static let image = UIImage(named: "TrainingHero.jpg")
}
struct HeroScene: View {
  var body: some View {
    GeometryReader { g in
      ZStack(alignment: .bottomTrailing) {
        if let image = HeroArtwork.image {
          Image(uiImage: image).resizable().scaledToFill().frame(
            width: g.size.width, height: g.size.height
          ).clipped()
        }
        if HeroArtwork.image == nil {
          LinearGradient(
            colors: [
              RAITheme.blue.opacity(0.15), RAITheme.amber.opacity(0.50),
              RAITheme.orange.opacity(0.15),
            ], startPoint: .topLeading, endPoint: .bottomTrailing)
          Circle().fill(
            RadialGradient(
              colors: [RAITheme.amber.opacity(0.75), .clear], center: .center, startRadius: 0,
              endRadius: 100)
          ).frame(width: 200, height: 200).offset(x: 45, y: -15)
          // Abstract track, never a fabricated geographic route.
          Path { p in
            p.move(to: CGPoint(x: g.size.width * 0.46, y: g.size.height))
            p.addQuadCurve(
              to: CGPoint(x: g.size.width, y: g.size.height * 0.32),
              control: CGPoint(x: g.size.width * 0.60, y: g.size.height * 0.58))
          }.stroke(RAITheme.amber.opacity(0.25), lineWidth: 2)
          Image(systemName: "figure.run").font(.system(size: 125, weight: .ultraLight))
            .foregroundStyle(.black.opacity(0.30)).offset(x: -12, y: 12)
        }
        LinearGradient(
          colors: [.black.opacity(0.05), .black.opacity(0.58)], startPoint: .top, endPoint: .bottom)
      }
    }.accessibilityHidden(true)
  }
}

struct RAITabMaterialFallback: ViewModifier {
  func body(content: Content) -> some View {
    if RAIRenderer.metalAvailable {
      content
    } else {
      content.toolbarBackground(.visible, for: .tabBar).toolbarBackground(
        RAITheme.brown, for: .tabBar)
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
