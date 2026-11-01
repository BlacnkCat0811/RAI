import Foundation
import XCTest
@testable import SyncCore

final class WorkoutPerformanceTests: XCTestCase {
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
            let result: [String: Any] = ["phase": phase, "minutes": minutes, "samples": samples.count, "gps": n, "json_bytes": raw.count, "gzip_bytes": zipped.count, "multipart_bytes": multipart.count, "plain_multipart_bytes": plainMultipart.count, "gzip_multipart_bytes": gzipMultipart.count, "temp_file_bytes": size, "build_ms": built.timeIntervalSince(begin)*1000, "encode_ms": encoded.timeIntervalSince(built)*1000, "gzip_ms": compressed.timeIntervalSince(encoded)*1000, "peak_rss": "NOT MEASURED", "environment": "macOS Swift package Debug; synthetic; not iPhone"]
            print("PHASE4_SWIFT=" + String(decoding: try JSONSerialization.data(withJSONObject: result, options: [.sortedKeys]), as: UTF8.self))
        }
    }
}
