import Foundation
import Testing
@testable import SyncCore

/// The Swift and Go contracts are validated against one shared synthetic
/// fixture, so a registry change on one side cannot silently diverge.
struct WorkoutMetadataRegistryTests {
    struct Fixture: Decodable {
        struct Key: Decodable {
            let key: String
            let kind: String
            let unit: String?
            let maxBytes: Int?
            let introducedIn: Int64
            let scopes: [String]
        }
        let contract: String
        let version: Int64
        let wireField: String
        let maxKeysPerObject: Int
        let textMaxBytes: Int
        let keys: [Key]
    }

    static func fixture() throws -> Fixture {
        let url = URL(filePath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent()
            .appending(path: "contracts/fixtures/workout-metadata-registry-v2.json")
        return try JSONDecoder().decode(Fixture.self, from: Data(contentsOf: url))
    }

    @Test func registryMatchesSharedContract() throws {
        let fixture = try Self.fixture()
        #expect(fixture.contract == "rai.workout.metadata_registry")
        #expect(fixture.wireField == "metadataRegistryVersion")
        #expect(fixture.version == WorkoutMetadataRegistry.version)
        #expect(fixture.maxKeysPerObject == WorkoutMetadataRegistry.maxKeysPerObject)
        #expect(fixture.textMaxBytes == WorkoutMetadataRegistry.textMaxBytes)
        #expect(WorkoutMetadataRegistry.entries.count == fixture.keys.count)
        for key in fixture.keys {
            let entry = WorkoutMetadataRegistry.entries.first { $0.key == key.key }
            let resolved = try #require(entry, "\(key.key) missing from the Swift registry")
            #expect(resolved.introducedIn == key.introducedIn)
            #expect(resolved.scopes.map(\.rawValue).sorted() == key.scopes.sorted())
            switch (key.kind, resolved.kind) {
            case ("text", .text(let maxBytes)): #expect(maxBytes == key.maxBytes)
            case ("boolean", .boolean): break
            case ("signedInteger", .signedInteger): break
            case ("quantity", .quantity(let unit)): #expect(unit == key.unit)
            default: Issue.record("\(key.key) kind mismatch: \(key.kind)")
            }
        }
    }

    @Test func opaqueKeysAreActivityOnly() {
        for entry in WorkoutMetadataRegistry.entries where entry.introducedIn > 1 {
            #expect(WorkoutMetadataRegistry.isOpaque(entry.key))
            #expect(entry.scopes == [.activity])
            #expect(WorkoutMetadataRegistry.entry(entry.key, scope: .workout) == nil)
            #expect(WorkoutMetadataRegistry.entry(entry.key, scope: .activityEvent) == nil)
            #expect(WorkoutMetadataRegistry.entry(entry.key, scope: .workoutEvent) == nil)
            #expect(WorkoutMetadataRegistry.entry(entry.key, scope: .route) == nil)
            #expect(WorkoutMetadataRegistry.entry(entry.key, scope: .observation) == nil)
            #expect(WorkoutMetadataRegistry.entry(entry.key, scope: .activity) != nil)
        }
        // A key known elsewhere is still not accepted at the wrong scope.
        #expect(WorkoutMetadataRegistry.isKnownElsewhere("HKIndoorWorkout"))
        #expect(WorkoutMetadataRegistry.entry("HKIndoorWorkout", scope: .activityEvent) != nil)
    }

    @Test func textContractIsBoundedAndUnnormalized() throws {
        try WorkoutMetadataRegistry.validateText("synthetic-step-path", maxBytes: 64)
        try WorkoutMetadataRegistry.validateText("synthetic-步-1", maxBytes: 64)
        #expect(throws: SyncFailure.self) { try WorkoutMetadataRegistry.validateText("", maxBytes: 64) }
        #expect(throws: SyncFailure.self) { try WorkoutMetadataRegistry.validateText(String(repeating: "a", count: 65), maxBytes: 64) }
        #expect(throws: SyncFailure.self) { try WorkoutMetadataRegistry.validateText("synthetic\npath", maxBytes: 64) }
        #expect(throws: SyncFailure.self) { try WorkoutMetadataRegistry.validateText("synthetic\u{7f}path", maxBytes: 64) }
        // Whitespace and case are content, not formatting: preserved as-is.
        try WorkoutMetadataRegistry.validateText(" Synthetic Step ", maxBytes: 64)
    }

    /// Same semantic metadata must produce the same canonical bytes no matter
    /// how the input dictionary was ordered.
    @Test func canonicalBytesIgnoreInputOrder() throws {
        let first: [String: JSON] = ["WOIntervalStepKeyPath": .string("synthetic-step-path"),
                                     "WOIntervalStepSuccessful": .int(1), "HKIndoorWorkout": .bool(true)]
        let second: [String: JSON] = ["HKIndoorWorkout": .bool(true),
                                      "WOIntervalStepSuccessful": .int(1), "WOIntervalStepKeyPath": .string("synthetic-step-path")]
        let a = try JSON.object(first).data(), b = try JSON.object(second).data()
        #expect(a == b)
        #expect(Wire.hash(a) == Wire.hash(b))
    }

    /// The registry version is declared on every bundle the client builds.
    @Test func bundleDeclaresRegistryVersion() throws {
        let scope = Scope(serviceID: UUID().uuidString.lowercased(), datasetID: UUID().uuidString.lowercased(),
                          datasetEpoch: UUID().uuidString.lowercased(), collectorEpoch: UUID().uuidString.lowercased())
        let workout: JSON = .object(["op": .string("upsert"), "kind": .string("workout"), "type": .string("HKWorkoutTypeIdentifier"),
                                     "uuid": .string(UUID().uuidString.lowercased()), "startUs": .int(1), "endUs": .int(2),
                                     "source": .object(["bundleID": .string("synthetic.test")])])
        let raw = try WorkoutBundleAssembler.data(scope: scope, workoutID: workout["uuid"].text!, workout: workout,
                                                  samples: [], series: [], routes: [], completeSampleTypes: [],
                                                  completeRoutes: false)
        let decoded = try JSON.decode(raw)
        #expect(decoded["metadataRegistryVersion"] == .int(WorkoutMetadataRegistry.version))
        #expect(decoded["protocolVersion"] == .int(3))
    }
}
