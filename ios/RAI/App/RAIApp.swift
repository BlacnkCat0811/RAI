import SwiftUI
import UIKit
import SyncCore

@MainActor @Observable
final class SyncModel {
    static let shared = SyncModel()
    var authorization = UserDefaults.standard.bool(forKey: "healthAuthorizationRequested") ? "Read access requested; availability is private" : "Not requested"
    var status = "Idle"
    var pending = 0
    var lastACK: Date?
    var lastSync: Date?
    var error: String?
    var busy = false
    private(set) var transfers: BackgroundTransfers?
    private var coordinator: SyncCoordinator?
    private var store: OutboxStore?
    private var task: Task<Void, Never>?
    private var retryTask: Task<Void, Never>?
    init() {
        do {
            let directory = try FileManager.default.url(for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil, create: true).appendingPathComponent("RAIV2Sync", isDirectory: true)
            let store = try OutboxStore(directory: directory)
            self.store = store; self.coordinator = SyncCoordinator(store: store)
            self.transfers = BackgroundTransfers(directory: directory)
        } catch { self.error = "Local state unavailable. Existing files were preserved." }
    }
    func authorize() {
        guard !busy, let coordinator else { return }
        busy = true
        task = Task {
            defer { busy = false }
            do {
                try await coordinator.authorize()
                authorization = "Read access requested; availability is private"
                UserDefaults.standard.set(true, forKey: "healthAuthorizationRequested")
            } catch { self.error = "HealthKit authorization request failed." }
        }
    }
    func start(readHealth: Bool = true) {
        guard !busy else { return }
        retryTask?.cancel(); retryTask = nil
        task = Task { await run(readHealth: readHealth) }
    }
    func run(readHealth: Bool) async {
        guard !busy, let coordinator, let store, let transfers else { return }
        busy = true; error = nil; status = "Synchronizing"
        let saved = await store.snapshot()
        pending = saved.pages.count; lastACK = saved.lastACK; lastSync = saved.lastSync
        defer { busy = false }
        do {
            guard let secret = try ClientConfiguration.load() else { throw SyncFailure.invalid("configure_development_api_first") }
            try await coordinator.sync(secret: secret, transfers: transfers, readHealth: readHealth)
            let snapshot = await store.snapshot()
            pending = snapshot.pages.count; lastACK = snapshot.lastACK; lastSync = snapshot.lastSync
            if let failure = snapshot.pages.compactMap(\.error).first { error = failure.code }
            if let readError = snapshot.readErrors.values.first { error = readError }
            status = error != nil ? "Some streams need attention" : (pending == 0 ? "Current bounded pass complete" : "Saved work pending")
            if let date = snapshot.pages.filter({ $0.error?.retryable == true }).map(\.nextAttempt).min() {
                let delay = max(1, min(60, date.timeIntervalSinceNow))
                retryTask = Task {
                    try? await Task.sleep(for: .seconds(delay))
                    guard !Task.isCancelled else { return }
                    await run(readHealth: false)
                }
            }
        } catch let failure as SyncFailure { error = String(describing: failure); status = "Stopped; local state retained" }
        catch let failure as UploadError {
            error = failure.code; status = "Waiting; local state retained"
            if failure.retryable {
                retryTask = Task { try? await Task.sleep(for: .seconds(max(1, min(failure.retryAfter ?? 8, 60)))); guard !Task.isCancelled else { return }; await run(readHealth: false) }
            }
        } catch is CancellationError { status = "Paused; local state retained" }
        catch { self.error = "HealthKit read or local encoding failed; anchor retained."; status = "Needs inspection" }
    }
    func retrySaved() {
        guard !busy, let store else { return }
        Task { do { try await store.retryBlocked(); start() } catch { self.error = "Local retry state could not be saved." } }
    }
    func suspendForegroundWork() { task?.cancel(); retryTask?.cancel() }
}

@MainActor
final class AppDelegate: NSObject, UIApplicationDelegate {
    func application(_ application: UIApplication, handleEventsForBackgroundURLSession identifier: String, completionHandler: @escaping () -> Void) {
        guard identifier == BackgroundTransfers.identifier, let transfers = SyncModel.shared.transfers else { completionHandler(); return }
        // Reconnect to the stable session immediately. The delegate persists
        // each result before this callback drains/reconciles local receipts.
        transfers.handleEvents {
            Task { @MainActor in
                await SyncModel.shared.run(readHealth: false)
                completionHandler()
            }
        }
    }
}

@main
struct RAIApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) var delegate
    @State private var model = SyncModel.shared
    @Environment(\.scenePhase) private var phase
    var body: some Scene {
        WindowGroup {
            ProductTabs(sync: model)
                .onChange(of: phase) { _, phase in
                    if phase == .active { model.start(readHealth: false) }
                    else if phase == .background { model.suspendForegroundWork() }
                }
        }
    }
}

struct SyncView: View {
    @Bindable var model: SyncModel
    @State private var apiURL = ""
    @State private var collector = ""
    @State private var token = ""
    var body: some View {
        Form {
            Section("Health Authorization") {
                Text(model.authorization).font(.footnote)
                Button("Request Read Access") { model.authorize() }.disabled(model.busy)
            }
            Section("Sync") {
                LabeledContent("Status", value: model.status)
                LabeledContent("Pending pages", value: String(model.pending))
                if let date = model.lastSync { LabeledContent("Last sync") { Text(date, style: .time) } }
                if let date = model.lastACK { LabeledContent("Last ACK") { Text(date, style: .time) } }
                if let error = model.error { Text(error).foregroundStyle(.red).font(.footnote) }
                Button("Sync Now") { model.start() }.disabled(model.busy)
                Button("Retry Saved Work") { model.retrySaved() }.disabled(model.busy)
            }
            Section("Development API") {
                TextField("HTTPS base URL", text: $apiURL).textInputAutocapitalization(.never).autocorrectionDisabled()
                TextField("Collector epoch UUID", text: $collector).textInputAutocapitalization(.never).autocorrectionDisabled()
                SecureField("Device bearer token", text: $token).textInputAutocapitalization(.never).autocorrectionDisabled()
                Button("Save to Keychain") {
                    do { try ClientConfiguration.save(baseURL: apiURL, collectorEpoch: collector, token: token); token = ""; model.error = nil }
                    catch { model.error = "Invalid configuration or Keychain write failed." }
                }.disabled(model.busy)
                Text("Use an isolated dataset. A saved dataset/collector identity cannot be silently changed.").font(.caption)
            }
        }.navigationTitle("同步与诊断")
            .scrollContentBackground(.hidden)
            .raiScreen()
    }
}
