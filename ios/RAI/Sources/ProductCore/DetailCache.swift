import Foundation

/// Last loaded Workout detail and series per Workout, kept on disk so the
/// detail page paints at once after an App restart and refreshes behind it.
/// Only a display cache: the server response always replaces it.
public struct WorkoutDetailDiskCache: Sendable {
  public struct Entry: Codable, Sendable {
    public var detail: WorkoutDetailResponse
    public var series: WorkoutSeriesResponse?
    public init(detail: WorkoutDetailResponse, series: WorkoutSeriesResponse?) {
      self.detail = detail
      self.series = series
    }
  }
  public let directory: URL
  public let limit: Int
  public init(directory: URL, limit: Int = 40) {
    self.directory = directory
    self.limit = limit
  }

  private func url(_ id: String) -> URL? {
    // Workout IDs are UUIDs; anything else is never used as a file name.
    guard let uuid = UUID(uuidString: id) else { return nil }
    return directory.appendingPathComponent(uuid.uuidString.lowercased() + ".json")
  }

  public func load(_ id: String) -> Entry? {
    guard let url = url(id), let data = try? Data(contentsOf: url),
      let entry = try? JSONDecoder().decode(Entry.self, from: data),
      entry.detail.workout.workoutID.lowercased() == id.lowercased()
    else { return nil }
    return entry
  }

  public func save(_ id: String, _ entry: Entry) {
    guard let url = url(id), let data = try? JSONEncoder().encode(entry) else { return }
    try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    try? data.write(to: url, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    prune()
  }

  /// Keeps the most recently written entries only.
  private func prune() {
    let fm = FileManager.default
    guard let files = try? fm.contentsOfDirectory(at: directory, includingPropertiesForKeys: [.contentModificationDateKey]),
      files.count > limit
    else { return }
    let dated = files.map { ($0, (try? $0.resourceValues(forKeys: [.contentModificationDateKey]).contentModificationDate) ?? .distantPast) }
    for (file, _) in dated.sorted(by: { $0.1 > $1.1 }).dropFirst(limit) {
      try? fm.removeItem(at: file)
    }
  }
}

/// Downsampled home route thumbnails on disk. Empty routes are not stored:
/// a route can arrive after its Workout, so "no route" is asked again.
public struct HomeRouteDiskCache: Sendable {
  public let url: URL
  public let limit: Int
  public init(url: URL, limit: Int = 30) {
    self.url = url
    self.limit = limit
  }
  private struct Stored: Codable {
    var order: [String]
    var routes: [String: [RoutePoint]]
  }
  public func load() -> [String: [RoutePoint]] {
    guard let data = try? Data(contentsOf: url), let stored = try? JSONDecoder().decode(Stored.self, from: data) else { return [:] }
    return stored.routes
  }
  public func save(_ id: String, _ route: [RoutePoint]) {
    guard !route.isEmpty else { return }
    var stored = (try? Data(contentsOf: url)).flatMap { try? JSONDecoder().decode(Stored.self, from: $0) } ?? Stored(order: [], routes: [:])
    stored.order.removeAll { $0 == id }
    stored.order.append(id)
    stored.routes[id] = route
    while stored.order.count > limit {
      stored.routes.removeValue(forKey: stored.order.removeFirst())
    }
    guard let data = try? JSONEncoder().encode(stored) else { return }
    try? FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
    try? data.write(to: url, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
  }
}

extension AthleteModelSnapshot {
  /// The server hides the whole day release while it recomputes it after a
  /// sync: every card is pending and none has a value.
  public var awaitingRecompute: Bool {
    let envelopes = [trainingLoad.envelope, physiologicalRecovery.envelope, trainingReadiness.envelope]
      + [runningFitness?.envelope, sessionRecovery?.envelope].compactMap { $0 }
    return !envelopes.contains(where: \.presentationValueAvailable)
      && envelopes.contains { $0.productReasonCodes.contains(ProductCode(rawValue: "model_result_pending")) }
  }
}

extension ProductSnapshot {
  /// While the day release is recomputed, keep showing the last complete
  /// release of the same model stack instead of blanking every card, and mark
  /// it as updating. Never mixes two releases: the whole snapshot is kept.
  public func keepingPublishedAthlete(from previous: ProductSnapshot) -> ProductSnapshot {
    var out = self
    // Newer servers already serve the previous complete release and say so.
    if let athlete, athlete.updating == true, !athlete.awaitingRecompute {
      out.athleteUpdating = true
      return out
    }
    guard let athlete, athlete.awaitingRecompute else {
      out.athleteUpdating = false
      return out
    }
    out.athleteUpdating = true
    if let last = previous.athlete, last.supportsPresentation, !last.awaitingRecompute,
      last.assignment.stackID == athlete.assignment.stackID
    {
      out.athlete = last
    }
    return out
  }
  /// The cards show the previous release while the server recomputes.
  public var athleteShowingPrevious: Bool { athleteUpdating && athlete?.awaitingRecompute == false }
}
