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
        .testTarget(name: "SyncCoreTests", dependencies: ["SyncCore"], resources: [.copy("Fixtures")])
    ], swiftLanguageModes: [.v6]
)
