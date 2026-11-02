// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "RAIClient", platforms: [.iOS(.v18), .macOS(.v15)],
    products: [.library(name: "SyncCore", targets: ["SyncCore"]), .library(name: "ProductCore", targets: ["ProductCore"])],
    targets: [
        .target(name: "RAICompression", linkerSettings: [.linkedLibrary("z")]),
        .target(name: "SyncCore", dependencies: ["RAICompression"]),
        .target(name: "ProductCore", dependencies: ["SyncCore"]),
        .testTarget(name: "ProductCoreTests", dependencies: ["ProductCore"]),
        .executableTarget(name: "ProductProbe", dependencies: ["ProductCore", "SyncCore"], path: "Tools/ProductProbe"),
        .executableTarget(name: "SyncProbe", dependencies: ["SyncCore"], path: "Tools/SyncProbe"),
        .executableTarget(name: "GCProbe", dependencies: ["SyncCore"], path: "Tools/GCProbe"),
        .executableTarget(name: "DailyProbe", dependencies: ["SyncCore"], path: "Tools/DailyProbe"),
        .testTarget(name: "SyncCoreTests", dependencies: ["SyncCore"], resources: [.copy("Fixtures")])
    ], swiftLanguageModes: [.v6]
)
