import XCTest
import HealthKit
import ObjectiveC
import SyncCore
@testable import RAI

/// R2 evidence for the normalization + persistence stage, measured on this host
/// with synthetic samples only.
///
/// Two different things are measured, and only the first is asserted:
///
/// 1. The per-sample CPU work each path performs. This is the work R2 removes
///    (per-sample `NSKeyedArchiver`) and replaces (canonical fact encoding), and
///    it is stable enough to compare: it touches no disk.
/// 2. The end-to-end capture stage including segment persistence. That number
///    is reported, not asserted, because the simulator's file stalls make it
///    vary by 3x between identical runs.
///
/// Build profile: Debug (simulator lane).
final class NormalizedCapturePerformanceTests: XCTestCase {
    private static let samples = 2_000
    private static let repetitions = 3

    private func perfSourceRevision(_ bundleID: String = "com.apple.health") -> HKSourceRevision {
        let source = class_createInstance(HKSource.self, 0) as! HKSource
        source.setValue(bundleID, forKey: "bundleIdentifier")
        source.setValue("Apple Watch", forKey: "name")
        return HKSourceRevision(source: source, version: "synthetic",
                                productType: "Watch7,1",
                                operatingSystemVersion: OperatingSystemVersion(majorVersion: 26,
                                                                               minorVersion: 0,
                                                                               patchVersion: 0))
    }

    /// Synthetic samples shaped like a watch-written workout: source revision,
    /// device descriptor, and allowlisted metadata. No real health values and
    /// no real device identifiers.
    private func perfSamples(_ count: Int) -> [HKQuantitySample] {
        let revision = perfSourceRevision()
        let unit = HKUnit.count().unitDivided(by: .minute())
        let base = Date(timeIntervalSince1970: 1_700_000_000)
        let device = HKDevice(name: "Apple Watch", manufacturer: "Apple Inc.",
                              model: "Watch", hardwareVersion: "Watch7,1",
                              firmwareVersion: "26.0", softwareVersion: "26.0",
                              localIdentifier: nil, udiDeviceIdentifier: nil)
        return (0..<count).map { index in
            let sample = HKQuantitySample(type: HKQuantityType(.heartRate),
                                          quantity: HKQuantity(unit: unit, doubleValue: Double(90 + index % 60)),
                                          start: base.addingTimeInterval(Double(index)),
                                          end: base.addingTimeInterval(Double(index) + 1),
                                          metadata: [HKMetadataKeyIndoorWorkout: true,
                                                     HKMetadataKeyTimeZone: "Asia/Shanghai",
                                                     HKMetadataKeyHeartRateMotionContext: 1])
            sample.setValue(revision, forKey: "sourceRevision")
            sample.setValue(device, forKey: "device")
            return sample
        }
    }

    private func freshDirectory(_ tag: String) throws -> URL {
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("rai-perf-" + tag + "-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        return url
    }

    private func milliseconds(_ body: () throws -> Void) rethrows -> Double {
        let started = DispatchTime.now().uptimeNanoseconds
        try body()
        return Double(DispatchTime.now().uptimeNanoseconds - started) / 1_000_000
    }

    private func millisecondsAsync(_ body: () async throws -> Void) async rethrows -> Double {
        let started = DispatchTime.now().uptimeNanoseconds
        try await body()
        return Double(DispatchTime.now().uptimeNanoseconds - started) / 1_000_000
    }

    /// The per-sample CPU work of both paths, minimum over repetitions.
    func testPerSampleNormalizationCostDrops() throws {
        let samples = perfSamples(Self.samples)
        let type = HKQuantityType(.heartRate).identifier

        var legacy: [Double] = []
        var normalized: [Double] = []
        for _ in 0..<Self.repetitions {
            var archives: [Data] = []
            let archive = try milliseconds {
                archives = try samples.map {
                    try NSKeyedArchiver.archivedData(withRootObject: $0, requiringSecureCoding: true)
                }
            }
            let hash = try milliseconds { for data in archives { _ = Wire.hash(data) } }
            let fingerprint = try milliseconds {
                for sample in samples { _ = try Normalizer.discoveryFingerprint(sample) }
            }
            legacy.append(archive + hash + fingerprint)

            var facts: [NormalizedFact] = []
            let build = try milliseconds {
                facts = try samples.map { try Normalizer.normalizedFact($0, type: type, workoutID: nil) }
            }
            let encode = try milliseconds { for fact in facts { _ = try fact.encoded() } }
            normalized.append(build + encode)
        }

        let legacyMin = legacy.min() ?? .infinity
        let normalizedMin = normalized.min() ?? .infinity
        print("R2-PERF cpu samples=\(Self.samples) repetitions=\(Self.repetitions) profile=Debug")
        print("R2-PERF cpu legacy_per_sample_ms=\(legacyMin / Double(Self.samples)) runs=\(legacy)")
        print("R2-PERF cpu normalized_per_sample_ms=\(normalizedMin / Double(Self.samples)) runs=\(normalized)")
        print("R2-PERF cpu ratio=\(legacyMin / normalizedMin)")

        XCTAssertLessThan(normalizedMin, legacyMin,
                          "normalized per-sample work was not cheaper than archiving, hashing and fingerprinting")
    }

    /// End-to-end capture including segment persistence. Reported, not asserted:
    /// the simulator's file stalls dominate this number and vary by 3x.
    func testCaptureStageEndToEndIsReported() async throws {
        let samples = perfSamples(Self.samples)
        let type = HKQuantityType(.heartRate).identifier
        var legacy: [Double] = []
        var normalized: [Double] = []
        var legacyArchives = 0
        let meterBefore = SampleArchiveMeter.snapshot()

        for repetition in 0..<Self.repetitions {
            let legacyDirectory = try freshDirectory("legacy-\(repetition)")
            defer { try? FileManager.default.removeItem(at: legacyDirectory) }
            let legacyStore = try OutboxStore(directory: legacyDirectory)
            legacy.append(try await millisecondsAsync {
                for start in stride(from: 0, to: samples.count, by: 1_000) {
                    let page = Array(samples[start..<min(start + 1_000, samples.count)])
                    var records: [CapturedFact] = []
                    records.reserveCapacity(page.count)
                    for sample in page {
                        let archive = try NSKeyedArchiver.archivedData(withRootObject: sample,
                                                                      requiringSecureCoding: true)
                        records.append(CapturedFact(type: type, uuid: sample.uuid.uuidString.lowercased(),
                                                    archive: archive,
                                                    identityHash: try Normalizer.discoveryFingerprint(sample)))
                        legacyArchives += 1
                    }
                    _ = try await legacyStore.captureFactSegment(type: type, workoutID: nil,
                                                                 records: records, publish: false)
                }
            })

            let normalizedDirectory = try freshDirectory("normalized-\(repetition)")
            defer { try? FileManager.default.removeItem(at: normalizedDirectory) }
            let normalizedStore = try OutboxStore(directory: normalizedDirectory)
            normalized.append(try await millisecondsAsync {
                for start in stride(from: 0, to: samples.count, by: 1_000) {
                    let page = Array(samples[start..<min(start + 1_000, samples.count)])
                    let facts = try page.map { try Normalizer.normalizedFact($0, type: type, workoutID: nil) }
                    _ = try await normalizedStore.captureNormalizedFacts(type: type, workoutID: nil,
                                                                         facts: facts, publish: false)
                }
            })
        }
        let normalizedArchives = SampleArchiveMeter.snapshot().archive - meterBefore.archive

        print("R2-PERF e2e samples=\(Self.samples) repetitions=\(Self.repetitions) profile=Debug")
        print("R2-PERF e2e legacy_min_ms=\(legacy.min() ?? 0) legacy_p50_ms=\(legacy.sorted()[legacy.count / 2]) runs=\(legacy)")
        print("R2-PERF e2e normalized_min_ms=\(normalized.min() ?? 0) normalized_p50_ms=\(normalized.sorted()[normalized.count / 2]) runs=\(normalized)")
        print("R2-PERF e2e legacy_sample_archive_calls=\(legacyArchives) normalized_meter_archive_calls=\(normalizedArchives)")

        XCTAssertEqual(legacyArchives, Self.samples * Self.repetitions,
                       "the legacy path did not archive every sample")
        XCTAssertEqual(normalizedArchives, 0, "the normalized path archived a sample")
    }

    /// R2-P01: the normalization + persistence stage itself, with the same
    /// fixture in the same process, old path against new path.
    ///
    /// Old path = per-sample `NSKeyedArchiver` + fingerprint + sealed segment
    /// write. New path = `Normalizer.normalizedFact` + canonical bytes + sealed
    /// segment write. Both persist through the same store API with the same
    /// 1000-record batching, so the difference is exactly what R2 removed.
    ///
    /// Seven repetitions, median (P50) per path. The 70% drop is asserted only
    /// in a Release build; a Debug run prints the numbers and says so, because
    /// an unoptimized build measures the interpreter, not the shipped client.
    /// Where the capture stage's per-sample time actually goes. Reported, never
    /// asserted: it exists so a regression in the normalization path is visible
    /// and so the R2-P01 ratio can be explained rather than guessed.
    func testCaptureComponentCostBreakdown() async throws {
        let samples = perfSamples(Self.samples)
        let type = HKQuantityTypeIdentifier.heartRate.rawValue
        func ms(_ body: () throws -> Void) rethrows -> Double {
            let started = DispatchTime.now().uptimeNanoseconds
            try body()
            return Double(DispatchTime.now().uptimeNanoseconds - started) / 1_000_000
        }
        var archiveBytes = 0
        let archiveMs = try ms {
            for sample in samples {
                archiveBytes += try NSKeyedArchiver.archivedData(withRootObject: sample,
                                                                 requiringSecureCoding: true).count
            }
        }
        let fingerprintMs = try ms {
            for sample in samples { _ = try Normalizer.discoveryFingerprint(sample) }
        }
        let factMs = try ms {
            for sample in samples { _ = try Normalizer.normalizedFact(sample, type: type, workoutID: nil) }
        }
        let facts = try samples.map { try Normalizer.normalizedFact($0, type: type, workoutID: nil) }
        let encodeMs = try ms {
            for fact in facts { _ = try fact.encoded() }
        }
        let count = Double(samples.count)
        print("R2-P01 components ms_per_sample archive=\(archiveMs / count) fingerprint=\(fingerprintMs / count) normalized_fact=\(factMs / count) fact_encode=\(encodeMs / count) archive_bytes_total=\(archiveBytes)")
    }

    func testNormalizationAndPersistenceP50DropsSeventyPercent() async throws {
        let samples = perfSamples(Self.samples)
        let type = HKQuantityType(.heartRate).identifier
        let repetitions = 7
        var legacy: [Double] = []
        var normalized: [Double] = []
        var legacyArchives = 0
        let meterBefore = SampleArchiveMeter.snapshot()

        for repetition in 0..<repetitions {
            let legacyDirectory = try freshDirectory("p50-legacy-\(repetition)")
            defer { try? FileManager.default.removeItem(at: legacyDirectory) }
            let legacyStore = try OutboxStore(directory: legacyDirectory)
            legacy.append(try await millisecondsAsync {
                for start in stride(from: 0, to: samples.count, by: 1_000) {
                    let page = Array(samples[start..<min(start + 1_000, samples.count)])
                    var records: [CapturedFact] = []
                    records.reserveCapacity(page.count)
                    for sample in page {
                        let archive = try NSKeyedArchiver.archivedData(withRootObject: sample,
                                                                      requiringSecureCoding: true)
                        records.append(CapturedFact(type: type, uuid: sample.uuid.uuidString.lowercased(),
                                                    archive: archive,
                                                    identityHash: try Normalizer.discoveryFingerprint(sample)))
                        legacyArchives += 1
                    }
                    _ = try await legacyStore.captureFactSegment(type: type, workoutID: nil,
                                                                 records: records, publish: false)
                }
            })

            let normalizedDirectory = try freshDirectory("p50-normalized-\(repetition)")
            defer { try? FileManager.default.removeItem(at: normalizedDirectory) }
            let normalizedStore = try OutboxStore(directory: normalizedDirectory)
            normalized.append(try await millisecondsAsync {
                for start in stride(from: 0, to: samples.count, by: 1_000) {
                    let page = Array(samples[start..<min(start + 1_000, samples.count)])
                    let facts = try page.map { try Normalizer.normalizedFact($0, type: type, workoutID: nil) }
                    _ = try await normalizedStore.captureNormalizedFacts(type: type, workoutID: nil,
                                                                         facts: facts, publish: false)
                }
            })
        }
        let normalizedArchives = SampleArchiveMeter.snapshot().archive - meterBefore.archive
        func p50(_ values: [Double]) -> Double { values.sorted()[values.count / 2] }
        let legacyP50 = p50(legacy), normalizedP50 = p50(normalized)
        let drop = (legacyP50 - normalizedP50) / legacyP50 * 100
        // The App test target needs the DEBUG-gated test hooks of the app
        // module, so an optimized run is built as Release (-O) with DEBUG
        // compilation conditions plus RAI_PERF_RELEASE; the label says exactly
        // that instead of pretending it is a plain Release build.
        #if RAI_PERF_RELEASE
        let profile = "Release(-O) + DEBUG conditions (test hooks)"
        #elseif DEBUG
        let profile = "Debug"
        #else
        let profile = "Release"
        #endif
        print("R2-P01 samples=\(Self.samples) repetitions=\(repetitions) profile=\(profile)")
        print("R2-P01 legacy_p50_ms=\(legacyP50) normalized_p50_ms=\(normalizedP50) drop_percent=\(drop) runs_legacy=\(legacy) runs_normalized=\(normalized)")
        print("R2-P01 legacy_sample_archive_calls=\(legacyArchives) normalized_meter_archive_calls=\(normalizedArchives)")

        XCTAssertEqual(legacyArchives, Self.samples * repetitions)
        XCTAssertEqual(normalizedArchives, 0)
        #if RAI_PERF_RELEASE
        XCTAssertGreaterThanOrEqual(drop, 70,
            "R2-P01 requires the normalization + persistence P50 to drop at least 70% in Release; measured \(drop)%")
        #endif
    }
}
