import Foundation
import SyncCore

// Synthetic macOS process harness. It never reads HealthKit or contacts a server.
@main struct GCProbe {
    static func main() async throws {
        let args = CommandLine.arguments
        guard args.count == 3 else { fatalError("GCProbe seed|sweep|verify DIRECTORY") }
        let directory = URL(fileURLWithPath: args[2]), store = try OutboxStore(directory: directory)
        if args[1] == "seed" {
            var operations: [OutboxOperation] = []
            for _ in 0..<256 {
                let file = try await store.savePayload(Data("synthetic".utf8))
                operations.append(.request(.init(method: "POST", path: "/v2/test", file: file)))
            }
            let page = PendingPage(anchorKey: "gc-test", candidateAnchor: Data([7]), operations: operations)
            try await store.stage(page)
            for i in operations.indices { try await store.acknowledge(page.id, cursor: i, receipt: .object([:])) }
        } else if args[1] == "sweep" {
            while try await store.collectGarbage(limit: 1, dryRun: false) > 0 {
                print("GC_BATCH_COMPLETE"); fflush(stdout)
                try await Task.sleep(for: .milliseconds(100))
            }
        } else if args[1] == "verify" {
            while try await store.collectGarbage(dryRun: false) > 0 {}
            let state = await store.snapshot()
            guard state.localAnchors["gc-test"] == Data([7]), state.pages.isEmpty, state.reclaimFiles?.isEmpty == true,
                  try FileManager.default.contentsOfDirectory(atPath: directory.path) == ["state.json"] else { fatalError("unsafe GC recovery") }
            print("P7_GC_RECOVERY=PASS (macOS synthetic SIGKILL between bounded sweep batches)")
        } else { fatalError("unknown mode") }
    }
}
