import Foundation
import CryptoKit
import RAICompression

public enum SyncFailure: Error, Equatable, Sendable {
    case invalid(String), receiptMismatch, localState(String)
}

public indirect enum JSON: Codable, Equatable, Sendable {
    case object([String: JSON]), array([JSON]), string(String), int(Int64), number(Double), bool(Bool), null
    public init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() { self = .null }
        else if let v = try? c.decode(Bool.self) { self = .bool(v) }
        else if let v = try? c.decode(Int64.self) { self = .int(v) }
        else if let v = try? c.decode(Double.self) { self = .number(v) }
        else if let v = try? c.decode(String.self) { self = .string(v) }
        else if let v = try? c.decode([JSON].self) { self = .array(v) }
        else { self = .object(try c.decode([String: JSON].self)) }
    }
    public func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .object(let v): try c.encode(v)
        case .array(let v): try c.encode(v)
        case .string(let v): try c.encode(v)
        case .int(let v): try c.encode(v)
        case .number(let v): try c.encode(v)
        case .bool(let v): try c.encode(v)
        case .null: try c.encodeNil()
        }
    }
    public subscript(_ key: String) -> JSON { if case .object(let o) = self { return o[key] ?? .null }; return .null }
    public var text: String? { if case .string(let s) = self { return s }; return nil }
    public var integer: Int64? { if case .int(let v) = self { return v }; return nil }
    public var list: [JSON] { if case .array(let a) = self { return a }; return [] }
    public var fields: [String: JSON] { if case .object(let o) = self { return o }; return [:] }
    public static func decode(_ data: Data) throws -> JSON { try JSONDecoder().decode(JSON.self, from: data) }
    public func data() throws -> Data {
        if AcceptanceSwitches.legacyEncoder { return try legacyData() }
        var buffer: [UInt8] = []
        buffer.reserveCapacity(256)
        try write(into: &buffer)
        return Data(buffer)
    }
    /// The previous encoder, kept only so an acceptance run can measure the
    /// same workload without the P3 optimisation. Bytes are identical to
    /// `write(into:)`; `WireEncodingTests` proves that.
    public func legacyData() throws -> Data {
        switch self {
        case .object(let o): return try Wire.object(o.keys.sorted().map { ($0, o[$0]!) }, legacy: true)
        case .array(let a): return try Data(("[" + a.map { String(decoding: try $0.legacyData(), as: UTF8.self) }.joined(separator: ",") + "]").utf8)
        case .string(let s):
            let encoder = JSONEncoder(); encoder.outputFormatting = .withoutEscapingSlashes
            let encoded = String(decoding: try encoder.encode(s), as: UTF8.self)
            return Data(encoded.replacingOccurrences(of: "<", with: "\\u003c").replacingOccurrences(of: ">", with: "\\u003e")
                .replacingOccurrences(of: "&", with: "\\u0026").replacingOccurrences(of: "\u{2028}", with: "\\u2028")
                .replacingOccurrences(of: "\u{2029}", with: "\\u2029").utf8)
        case .int(let v): return Data(String(v).utf8)
        case .number(let v): return Data(try Wire.goNumber(v).utf8)
        case .bool(let b): return Data((b ? "true" : "false").utf8)
        case .null: return Data("null".utf8)
        }
    }
    /// Canonical byte writer. One bounded buffer, no per-node `Data`, `String`
    /// or `JSONEncoder` allocation. The bytes are identical to the previous
    /// implementation (sorted object keys, Go float formatting, the same string
    /// escaping); `WireEncodingTests` proves that against the reference encoder.
    public func write(into buffer: inout [UInt8]) throws {
        switch self {
        case .object(let o):
            buffer.append(0x7B) // {
            var separator = false
            for key in o.keys.sorted() {
                if separator { buffer.append(0x2C) }
                separator = true
                Wire.appendString(key, to: &buffer)
                buffer.append(0x3A)
                try o[key]!.write(into: &buffer)
            }
            buffer.append(0x7D) // }
        case .array(let a):
            buffer.append(0x5B) // [
            var separator = false
            for value in a {
                if separator { buffer.append(0x2C) }
                separator = true
                try value.write(into: &buffer)
            }
            buffer.append(0x5D) // ]
        case .string(let s): Wire.appendString(s, to: &buffer)
        case .int(let v): buffer.append(contentsOf: String(v).utf8)
        case .number(let v): buffer.append(contentsOf: try Wire.goNumber(v).utf8)
        case .bool(let b): buffer.append(contentsOf: b ? Wire.trueBytes : Wire.falseBytes)
        case .null: buffer.append(contentsOf: Wire.nullBytes)
        }
    }
}

public enum Wire {
    public static func id() -> String { UUID().uuidString.lowercased() }
    public static func digest(_ data: Data) -> Data { Data(SHA256.hash(data: data)) }
    public static func hex(_ data: Data) -> String { data.map { String(format: "%02x", $0) }.joined() }
    public static func hash(_ data: Data) -> String { hex(digest(data)) }
    public static func chain(_ previous: Data, _ digest: Data) -> Data { self.digest(previous + digest) }
    public static func object(_ fields: [(String, JSON)], legacy: Bool = false) throws -> Data {
        if legacy {
            return try Data(("{" + fields.map { key, value in
                String(decoding: try JSON.string(key).legacyData(), as: UTF8.self) + ":" + String(decoding: try value.legacyData(), as: UTF8.self)
            }.joined(separator: ",") + "}").utf8)
        }
        var buffer: [UInt8] = []
        buffer.reserveCapacity(256)
        buffer.append(0x7B) // {
        var separator = false
        for (key, value) in fields {
            if separator { buffer.append(0x2C) }
            separator = true
            appendString(key, to: &buffer)
            buffer.append(0x3A)
            try value.write(into: &buffer)
        }
        buffer.append(0x7D) // }
        return Data(buffer)
    }
    static let trueBytes: [UInt8] = [0x74, 0x72, 0x75, 0x65]
    static let falseBytes: [UInt8] = [0x66, 0x61, 0x6C, 0x73, 0x65]
    static let nullBytes: [UInt8] = [0x6E, 0x75, 0x6C, 0x6C]
    private static let quote: UInt8 = 0x22
    private static let hexLower: [UInt8] = Array("0123456789abcdef".utf8)
    private static let escapeQuote: [UInt8] = [0x5C, 0x22]
    private static let escapeBackslash: [UInt8] = [0x5C, 0x5C]
    private static let escapeNewline: [UInt8] = [0x5C, 0x6E]
    private static let escapeReturn: [UInt8] = [0x5C, 0x72]
    private static let escapeTab: [UInt8] = [0x5C, 0x74]
    private static let escapeBackspace: [UInt8] = [0x5C, 0x62]
    private static let escapeFormFeed: [UInt8] = [0x5C, 0x66]
    private static let escapeLess: [UInt8] = Array("\\u003c".utf8)
    private static let escapeGreater: [UInt8] = Array("\\u003e".utf8)
    private static let escapeAmp: [UInt8] = Array("\\u0026".utf8)
    private static let escape2028: [UInt8] = Array("\\u2028".utf8)
    private static let escape2029: [UInt8] = Array("\\u2029".utf8)
    /// JSON string bytes identical to `JSONEncoder` with `.withoutEscapingSlashes`
    /// plus the `<`, `>`, `&`, U+2028 and U+2029 hardening the wire contract
    /// requires. Writes from the UTF-8 view: no intermediate `String` or
    /// `JSONEncoder` is created per value.
    public static func appendString(_ value: String, to buffer: inout [UInt8]) {
        buffer.append(quote)
        let utf8 = value.utf8
        var index = utf8.startIndex
        let end = utf8.endIndex
        while index < end {
            let byte = utf8[index]
            switch byte {
            case 0x22: buffer.append(contentsOf: escapeQuote)
            case 0x5C: buffer.append(contentsOf: escapeBackslash)
            case 0x0A: buffer.append(contentsOf: escapeNewline)
            case 0x0D: buffer.append(contentsOf: escapeReturn)
            case 0x09: buffer.append(contentsOf: escapeTab)
            case 0x08: buffer.append(contentsOf: escapeBackspace)
            case 0x0C: buffer.append(contentsOf: escapeFormFeed)
            case 0x3C: buffer.append(contentsOf: escapeLess)
            case 0x3E: buffer.append(contentsOf: escapeGreater)
            case 0x26: buffer.append(contentsOf: escapeAmp)
            case 0xE2:
                // U+2028 and U+2029 arrive as E2 80 A8 and E2 80 A9.
                let next = utf8.index(after: index)
                if next < end, utf8[next] == 0x80 {
                    let third = utf8.index(after: next)
                    if third < end, utf8[third] == 0xA8 { buffer.append(contentsOf: escape2028); index = third }
                    else if third < end, utf8[third] == 0xA9 { buffer.append(contentsOf: escape2029); index = third }
                    else { buffer.append(byte) }
                } else { buffer.append(byte) }
            default:
                if byte < 0x20 {
                    buffer.append(0x5C); buffer.append(0x75); buffer.append(0x30); buffer.append(0x30)
                    buffer.append(hexLower[Int(byte >> 4)]); buffer.append(hexLower[Int(byte & 0x0F)])
                } else { buffer.append(byte) }
            }
            index = utf8.index(after: index)
        }
        buffer.append(quote)
    }
    // Match Go encoding/json float64 formatting: shortest roundtrip, decimal
    // for [1e-6,1e21), otherwise exponent without a leading zero. No rounding.
    public static func goNumber(_ value: Double) throws -> String {
        guard value.isFinite else { throw SyncFailure.invalid("nonfinite_number") }
        if value == 0 { return value.sign == .minus ? "-0" : "0" }
        let sign = value < 0 ? "-" : ""
        let parts = String(abs(value)).lowercased().split(separator: "e")
        let mantissa = String(parts[0])
        let split = mantissa.split(separator: ".", omittingEmptySubsequences: false)
        var digits = split.joined()
        var position = split[0].count + (parts.count == 2 ? Int(parts[1])! : 0)
        while digits.first == "0" { digits.removeFirst(); position -= 1 }
        while digits.last == "0" { digits.removeLast() }
        if abs(value) < 1e-6 || abs(value) >= 1e21 {
            let tail = digits.dropFirst()
            let exponent = position - 1
            return sign + String(digits.prefix(1)) + (tail.isEmpty ? "" : "." + tail) + "e" + (exponent >= 0 ? "+" : "") + String(exponent)
        }
        if position <= 0 { return sign + "0." + String(repeating: "0", count: -position) + digits }
        if position >= digits.count { return sign + digits + String(repeating: "0", count: position - digits.count) }
        return sign + digits.prefix(position) + "." + digits.dropFirst(position)
    }
    public static func gzip(_ data: Data) throws -> Data {
        var output: UnsafeMutablePointer<UInt8>?
        var count = 0
        let result = data.withUnsafeBytes { bytes in
            rai_gzip(bytes.bindMemory(to: UInt8.self).baseAddress, data.count, &output, &count)
        }
        guard result == 0, let output else { throw SyncFailure.invalid("gzip_failed") }
        defer { rai_free(output) }
        return Data(bytes: output, count: count)
    }
    /// Gzip transport limits for one WorkoutBundle, mirrored from the server
    /// contract (`apps/server/internal/httpapi/bundle_body.go`). The client
    /// enforces the same bounds so an illegal artifact fails locally and
    /// diagnosably instead of degrading to a plaintext upload.
    public enum GzipBundle {
        public static let maxDecodedBytes = 32 << 20
        public static let maxCompressedBytes = 8 << 20
        public static let ratioCap = 100
        public static let ratioSlack = 64 << 10
        /// The single gzip member must stay inside every server limit.
        public static func validate(decoded: Int, compressed: Int) throws {
            guard decoded <= maxDecodedBytes else { throw SyncFailure.localState("bundle_too_large") }
            guard compressed <= maxCompressedBytes else { throw SyncFailure.localState("bundle_compressed_too_large") }
            guard decoded <= ratioCap * compressed + ratioSlack else { throw SyncFailure.localState("bundle_compression_ratio_exceeded") }
        }
    }
}

public struct Scope: Codable, Equatable, Sendable {
    public let serviceID: String, datasetID: String, datasetEpoch: String, collectorEpoch: String
    public init(serviceID: String, datasetID: String, datasetEpoch: String, collectorEpoch: String) {
        self.serviceID = serviceID; self.datasetID = datasetID; self.datasetEpoch = datasetEpoch; self.collectorEpoch = collectorEpoch
    }
    public var fields: [String: JSON] { ["protocolVersion": .int(2), "datasetID": .string(datasetID), "datasetEpoch": .string(datasetEpoch), "collectorEpoch": .string(collectorEpoch)] }
    public var bundleFields: [String: JSON] { ["protocolVersion": .int(3), "datasetID": .string(datasetID), "datasetEpoch": .string(datasetEpoch), "collectorEpoch": .string(collectorEpoch)] }
}

public struct Envelope: Codable, Equatable, Sendable {
    public let protocolVersion: Int
    public let datasetID: String, datasetEpoch: String, collectorEpoch: String, streamID: String, batchID: String
    public let clientSeq: Int64
    public let previousBatchID: String?
    public let pageID: String
    public let fragmentIndex: Int, fragmentCount: Int
    public let queryFingerprint: String, contentEncoding: String
    public let wireBytes: Int, decodedBytes: Int, eventCount: Int
    public let payloadSHA256: String
    public func canonical() throws -> Data {
        try Wire.object([
            ("protocolVersion", .int(Int64(protocolVersion))), ("datasetID", .string(datasetID)), ("datasetEpoch", .string(datasetEpoch)),
            ("collectorEpoch", .string(collectorEpoch)), ("streamID", .string(streamID)), ("batchID", .string(batchID)),
            ("clientSeq", .int(clientSeq)), ("previousBatchID", previousBatchID.map(JSON.string) ?? .null), ("pageID", .string(pageID)),
            ("fragmentIndex", .int(Int64(fragmentIndex))), ("fragmentCount", .int(Int64(fragmentCount))), ("queryFingerprint", .string(queryFingerprint)),
            ("contentEncoding", .string(contentEncoding)), ("wireBytes", .int(Int64(wireBytes))), ("decodedBytes", .int(Int64(decodedBytes))),
            ("eventCount", .int(Int64(eventCount))), ("payloadSHA256", .string(payloadSHA256))
        ])
    }
    public func validateReceipt(_ receipt: JSON) throws {
        guard receipt["ingestStatus"].text == "committed", receipt["batchID"].text == batchID,
              receipt["datasetEpoch"].text == datasetEpoch, receipt["clientSeq"].integer == clientSeq,
              receipt["payloadSHA256"].text == payloadSHA256, let revision = receipt["commitRevision"].integer, revision >= 0,
              ["queued", "not_required"].contains(receipt["analysisStatus"].text ?? ""),
              case .object(let counts) = receipt["counts"], case .array(let jobs) = receipt["analysisJobIDs"] else { throw SyncFailure.receiptMismatch }
        // The server counts collapsed entity outcomes, so duplicate input
        // events can legitimately produce fewer outcomes than eventCount.
        var remaining = Int64(eventCount)
        for key in ["inserted", "unchanged", "deleted", "tombstoned", "suppressed"] {
            guard let count = counts[key]?.integer, count >= 0, count <= remaining else { throw SyncFailure.receiptMismatch }
            remaining -= count
        }
        for job in jobs {
            guard let id = job.text, let uuid = UUID(uuidString: id), uuid.uuidString.lowercased() == id,
                  id != "00000000-0000-0000-0000-000000000000" else { throw SyncFailure.receiptMismatch }
        }
    }
}
