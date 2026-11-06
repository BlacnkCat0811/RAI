import Foundation
import XCTest
@testable import SyncCore

#if canImport(Darwin)
import Darwin
#endif

final class WorkoutPerformanceTests: XCTestCase {
    /// Peak resident set of this process so far, in bytes.
    ///
    /// `ru_maxrss` is a high-water mark for the whole process, so on macOS it
    /// covers every tier already executed by the same test run. It is a real
    /// measurement, but it is a *process* peak, not the peak of one tier, and
    /// it is not the RSS of an iPhone sync session.
    private func peakResidentBytes() -> Int {
        #if canImport(Darwin)
        var usage = rusage()
        guard getrusage(RUSAGE_SELF, &usage) == 0 else { return -1 }
        #if os(macOS)
        return Int(usage.ru_maxrss)  // macOS reports bytes
        #else
        return Int(usage.ru_maxrss) * 1024  // Linux reports kilobytes
        #endif
        #else
        return -1
        #endif
    }

    /// Resident set right now, in bytes, so one tier's own footprint can be
    /// separated from the process high-water mark above.
    private func currentResidentBytes() -> Int {
        #if canImport(Darwin)
        var info = mach_task_basic_info()
        var count = mach_msg_type_number_t(MemoryLayout<mach_task_basic_info>.size / MemoryLayout<natural_t>.size)
        let status = withUnsafeMutablePointer(to: &info) { pointer in
            pointer.withMemoryRebound(to: integer_t.self, capacity: Int(count)) { rebound in
                task_info(mach_task_self_, task_flavor_t(MACH_TASK_BASIC_INFO), rebound, &count)
            }
        }
        return status == KERN_SUCCESS ? Int(info.resident_size) : -1
        #else
        return -1
        #endif
    }

    func testDiscoveryAssociationScaling() async throws {
        guard ProcessInfo.processInfo.environment["RAI_LOCAL_ASSOCIATION_PERF"] == "1" else { throw XCTSkip("explicit local synthetic association performance run") }
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-association-perf-" + Wire.id())
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        var discovery = DiscoveryState()
        var keys: [String] = []
        for i in 0..<10_000 {
            let event = DiscoveryEvent(type: "synthetic-sample", uuid: Wire.id(), deleted: true)
            discovery.events[event.id] = event
            if i < 1_000 { keys.append(event.factKey) }
        }
        var state = LocalState(); state.discovery = discovery
        try OutboxStore.write(JSONEncoder().encode(state), to: directory.appendingPathComponent("state.json"))
        let store = try OutboxStore(directory: directory), parent = Wire.id(), started = Date()
        try await store.associateDiscoveredFacts(workoutID: parent, keys: keys)
        let elapsed = Date().timeIntervalSince(started) * 1000
        let after = await store.discoverySnapshot()
        XCTAssertEqual(after.events.values.filter { $0.workoutID == parent }.count, keys.count)
        XCTAssertEqual(after.work[parent]?.generation, Int64(keys.count))
        print("LOCAL_ASSOCIATION_PERF events=10000 keys=1000 elapsed_ms=\(elapsed) environment=macOS_Swift_Debug_synthetic")
    }

    func testDurationMatrix() throws {
        guard let phase = ProcessInfo.processInfo.environment["RAI_PHASE4_PERF"] else { throw XCTSkip("explicit synthetic performance run") }
        for minutes in [20, 40, 60, 120, 240] {
            let begin = Date()
            let scope = Scope(serviceID: UUID().uuidString.lowercased(), datasetID: UUID().uuidString.lowercased(), datasetEpoch: UUID().uuidString.lowercased(), collectorEpoch: UUID().uuidString.lowercased())
            let id = UUID().uuidString.lowercased(), start: Int64 = 1793433600000000, end = start + Int64(minutes)*60*1000000
            let source: JSON = .object(["bundleID": .string("com.apple.health")])
            let observedSource: JSON = .object(["bundleID": .string("com.apple.health"), "name": .string("Synthetic Watch source"), "version": .string("1"), "productType": .string("Watch7,1"), "device": .object(["manufacturer": .string("Apple Inc."), "model": .string("Watch"), "hardwareVersion": .string("Watch7,1"), "softwareVersion": .string("11")])])
            let workout: JSON = .object(["op": .string("upsert"), "kind": .string("workout"), "type": .string("HKWorkoutTypeIdentifier"), "uuid": .string(id), "startUs": .int(start), "endUs": .int(end), "source": source, "workout": .object(["activityType": .int(37), "durationUs": .int(end-start)])])
            let types = ["HeartRate", "RunningPower", "StepCount", "DistanceWalkingRunning", "RunningStrideLength", "RunningGroundContactTime", "RunningVerticalOscillation"]
            let units = ["count/min", "W", "count", "m", "m", "ms", "cm"], density = [456,887,887,884,423,428,432]
            var samples: [JSON] = [], provenance: [JSON] = []
            for k in types.indices { let n = density[k]*minutes/40
                for i in 0..<n { let sid = UUID().uuidString.lowercased(), typ = "HKQuantityTypeIdentifier"+types[k], stamp = start + Int64(i)*Int64(minutes)*60*1000000/Int64(n)
                    samples.append(.object(["op": .string("upsert"), "kind": .string("sample"), "type": .string(typ), "uuid": .string(sid), "startUs": .int(stamp), "endUs": .int(stamp), "source": source, "quantity": .object(["value": .int(Int64(100+i%50)), "unit": .string(units[k])])]))
                    provenance.append(.object(["sampleType": .string(typ), "sampleUUID": .string(sid), "source": observedSource]))
                }
            }
            let routeID = UUID().uuidString.lowercased(), n = 2283*minutes/40
            var points: [JSON] = []
            for i in 0..<n {
                let stamp = start + Int64(i)*Int64(minutes)*60*1000000/Int64(n)
                let latitude = 31.2 + Double(i%100)*0.00001
                points.append(.object(["recordedAtUs": .int(stamp), "latitude": .number(latitude), "longitude": .number(121.4)]))
            }
            let routes: [JSON] = [.object(["routeID": .string(routeID), "source": source, "points": .array(points)])]
            let observation: JSON = .object(["version": .int(1), "observedAtUs": .int(1), "provenance": .object(["workout": observedSource, "samples": .array(provenance), "routes": .array([.object(["routeID": .string(routeID), "source": observedSource])])]), "metadata": .object(["HKIndoorWorkout": .bool(false)]), "coverage": .object(["power": .object(["state": .string("no_records_observed"), "sampleCount": .int(0), "queryCompleted": .bool(true), "localPersisted": .bool(true)])])])
            let built = Date(), raw = try WorkoutBundleAssembler.data(scope: scope, workoutID: id, workout: workout, samples: samples, series: [], routes: routes, completeSampleTypes: [], completeRoutes: false, observation: observation)
            let encoded = Date(), zipped = try Wire.gzip(raw), compressed = Date()
            let plainMultipart = WorkoutBundleAssembler.multipart(bundle: raw, boundary: "rai.phase4.synthetic")
            let gzipMultipart = WorkoutBundleAssembler.multipart(bundle: zipped, boundary: "rai.phase4.synthetic", compressed: true)
            let multipart = phase == "after" ? gzipMultipart : plainMultipart
            let file = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
            try multipart.write(to: file, options: .atomic); let size = try file.resourceValues(forKeys: [.fileSizeKey]).fileSize!; try FileManager.default.removeItem(at: file)
            let result: [String: Any] = ["phase": phase, "minutes": minutes, "samples": samples.count, "gps": n, "json_bytes": raw.count, "gzip_bytes": zipped.count, "multipart_bytes": multipart.count, "plain_multipart_bytes": plainMultipart.count, "gzip_multipart_bytes": gzipMultipart.count, "temp_file_bytes": size, "build_ms": built.timeIntervalSince(begin)*1000, "encode_ms": encoded.timeIntervalSince(built)*1000, "gzip_ms": compressed.timeIntervalSince(encoded)*1000,
                // Real measurements, but scoped: ru_maxrss is a process-wide
                // high-water mark that includes earlier tiers, and neither value
                // is the RSS of an iPhone sync session (P12 is device-scoped).
                "process_peak_rss_bytes": peakResidentBytes(),
                "resident_after_encode_bytes": currentResidentBytes(),
                "rss_scope": "macOS test process; process peak includes earlier tiers; not iPhone",
                "environment": "macOS Swift package Debug; synthetic; not iPhone"]
            print("PHASE4_SWIFT=" + String(decoding: try JSONSerialization.data(withJSONObject: result, options: [.sortedKeys]), as: UTF8.self))
        }
    }
}
