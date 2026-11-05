import Foundation

/// The single-request Workout import unit. Association membership is the
/// presence of a sample in `samples`; no separate per-sample request exists.
/// Completeness fields describe read coverage; missing children are retained.
public enum WorkoutBundleAssembler {
    /// Reuse an original v3 request only when the entire rebuilt payload can
    /// reproduce its exact durable identity. Observation time alone is not a
    /// HealthKit change. The receipt supplies the old capture time; its hash
    /// must still match every fact, source, coverage field and session byte.
    public static func exactReplay(rebuilt: Data, receipt: JSON) throws -> Data? {
        var fields = try JSON.decode(rebuilt).fields
        guard fields["protocolVersion"]?.integer == 3,
              let id = fields["workoutID"]?.text,
              receipt["sourceObservation"]["version"].integer == 1,
              let observedAt = receipt["sourceObservation"]["observedAtUs"].integer,
              case .object(var observation) = fields["observation"] else { return nil }
        observation["observedAtUs"] = .int(observedAt)
        fields["observation"] = .object(observation)
        let original = try JSON.object(fields).data(), hash = Wire.hash(original)
        guard receipt["bundleSHA256"].text == hash else { return nil }
        try WorkoutBundleACK.validate(receipt, workoutID: id, bundleHash: hash,
            sampleDeletions: fields["sampleDeletions"]?.list.count ?? 0,
            routeDeletions: fields["routeDeletions"]?.list.count ?? 0)
        return original
    }

    public static func data(scope: Scope, workoutID: String, workout: JSON, samples: [JSON],
                            series: [JSON], routes: [JSON], completeSampleTypes: [String],
                            completeRoutes: Bool, observation: JSON? = nil, sampleDeletions: [JSON] = [], routeDeletions: [String] = [], deletionOnly: Bool = false) throws -> Data {
        guard !workoutID.isEmpty else { throw SyncFailure.invalid("invalid_workout_id") }
        var request = scope.bundleFields
        request.merge([
            "workoutID": .string(workoutID),
            "workout": workout,
            "samples": .array(samples),
            "series": .array(series),
            "routes": .array(routes),
            "completeSampleTypes": .array(completeSampleTypes.map(JSON.string)),
            "completeRoutes": .bool(completeRoutes),
        ]) { _, new in new }
        if deletionOnly { request["deletionOnly"] = .bool(true) }
        if let observation { request["observation"] = observation }
        if !sampleDeletions.isEmpty { request["sampleDeletions"] = .array(sampleDeletions) }
        if !routeDeletions.isEmpty { request["routeDeletions"] = .array(routeDeletions.map(JSON.string)) }
        return try JSON.object(request).data()
    }

    public static func multipart(bundle: Data, boundary: String, compressed: Bool = false) -> Data {
        let filename = compressed ? "workout.json.gz" : "workout.json"
        let mediaType = compressed ? "application/gzip" : "application/json"
        var body = Data(("--" + boundary + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"" + filename + "\"\r\nContent-Type: " + mediaType + "\r\n\r\n").utf8)
        body.append(bundle)
        body.append(Data(("\r\n--" + boundary + "--\r\n").utf8))
        return body
    }
}
