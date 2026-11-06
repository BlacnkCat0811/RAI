import Foundation
import Testing
@testable import SyncCore

/// Byte identity for the canonical writer.
///
/// `JSON.write(into:)` replaced the previous per-node `Data`/`String`/
/// `JSONEncoder` implementation. These tests keep the previous implementation
/// as an executable reference and prove the new writer emits exactly the same
/// bytes, so an existing bundle hash can never change because of the
/// optimisation.
struct WireEncodingTests {
    // MARK: - Reference implementation (the previous encoder, unchanged)

    private func referenceData(_ value: JSON) throws -> Data {
        switch value {
        case .object(let o): return try referenceObject(o.keys.sorted().map { ($0, o[$0]!) })
        case .array(let a): return try Data(("[" + a.map { String(decoding: try referenceData($0), as: UTF8.self) }.joined(separator: ",") + "]").utf8)
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
    private func referenceObject(_ fields: [(String, JSON)]) throws -> Data {
        try Data(("{" + fields.map { key, value in
            String(decoding: try referenceData(.string(key)), as: UTF8.self) + ":" + String(decoding: try referenceData(value), as: UTF8.self)
        }.joined(separator: ",") + "}").utf8)
    }
    private func assertIdentical(_ value: JSON, _ label: String) throws {
        let reference = try referenceData(value), produced = try value.data()
        #expect(reference == produced, "byte mismatch for \(label): reference=\(String(decoding: reference, as: UTF8.self)) produced=\(String(decoding: produced, as: UTF8.self))")
        #expect(Wire.hash(reference) == Wire.hash(produced), "hash mismatch for \(label)")
    }

    /// The acceptance rollback switch must reproduce the same bytes, otherwise a
    /// baseline run would upload a different bundle hash.
    @Test func legacySwitchPathMatchesReference() throws {
        let corpus: [JSON] = [.string("lt<gt>amp& ls\u{2028}ps\u{2029} \"q\" \\ b"), .int(-9_223_372_036_854_775_808),
            .number(-0.0), .number(5e-324), .bool(true), .null,
            .array([.int(1), .string("跑步"), .number(1e21)]),
            .object(["b": .int(2), "a": .object(["z": .null, "y": .string("\u{1}")])])]
        for value in corpus {
            let reference = try referenceData(value), legacy = try value.legacyData()
            #expect(reference == legacy, "legacy mismatch for \(String(decoding: legacy, as: UTF8.self))")
        }
    }

    // MARK: - Strings

    @Test func stringEscapingMatchesReference() throws {
        var corpus: [String] = ["", "plain", "quote\"inside", "back\\slash", "line\nbreak", "tab\there",
            "cr\rhere", "backspace\u{08}here", "formfeed\u{0C}here", "lt<gt>amp&", "ls\u{2028}ps\u{2029}",
            "héllo wörld", "跑步记录", "🏃‍♀️ emoji 😀", "del\u{7F}", "/slash/not/escaped",
            String(repeating: "a", count: 5_000), "\u{1F600}\u{2028}\u{2029}<>&\"\\\n"]
        for code in 0x00...0x1F { corpus.append("ctl" + String(UnicodeScalar(UInt8(code))) + "end") }
        for value in corpus { try assertIdentical(.string(value), "string \(value.debugDescription)") }
    }

    @Test func randomizedStringsMatchReference() throws {
        var generator = SystemRandomNumberGenerator()
        let pools: [[Unicode.Scalar]] = [
            Array("abcdefghij\"\\<>&/".unicodeScalars),
            Array("éüñß跑步🏃".unicodeScalars),
            (0x00...0x1F).compactMap { Unicode.Scalar($0) },
            (0x2020...0x2030).compactMap { Unicode.Scalar($0) },
        ]
        for _ in 0..<300 {
            let pool = pools.randomElement(using: &generator)!
            let length = Int.random(in: 0...40, using: &generator)
            var text = ""
            for _ in 0..<length { text.unicodeScalars.append(pool.randomElement(using: &generator)!) }
            try assertIdentical(.string(text), "random \(text.debugDescription)")
        }
    }

    // MARK: - Scalars and containers

    @Test func scalarsAndContainersMatchReference() throws {
        let numbers: [Double] = [0, -0.0, 1, -1, 0.1, -0.1, 1e-7, 1e21, 1e-6, 12_345.678_9, .greatestFiniteMagnitude,
                                 .leastNormalMagnitude, .leastNonzeroMagnitude, 1.7976931348623157e308, 3.141592653589793,
                                 -2.718281828459045, 1e300, 5e-324, 0.30000000000000004, 2.0, 100.0, 1e6, 1e20]
        for value in numbers { try assertIdentical(.number(value), "number \(value)") }
        for value: Int64 in [0, -1, 1, Int64.max, Int64.min, 1_000_000] { try assertIdentical(.int(value), "int \(value)") }
        try assertIdentical(.bool(true), "bool"); try assertIdentical(.bool(false), "bool"); try assertIdentical(.null, "null")
        try assertIdentical(.array([]), "empty array"); try assertIdentical(.object([:]), "empty object")
        // Unsorted input still writes sorted keys, exactly like before.
        try assertIdentical(.object(["b": .int(2), "a": .int(1), "A": .string("x"), "é": .null]), "sorted keys")
        try assertIdentical(.array([.null, .bool(true), .string("x"), .array([.int(1), .object(["k": .string("<")])])]), "nested")
        try assertIdentical(.object(["key\"with<escapes>": .string("value&here"),
                                     "nested": .array((0..<20).map { .object(["i": .int(Int64($0)), "s": .string("v\($0)")]) })]), "mixed")
    }

    @Test func hardenedEscapesAreExplicit() throws {
        #expect(String(decoding: try JSON.string("a<b").data(), as: UTF8.self) == "\"a\\u003cb\"")
        #expect(String(decoding: try JSON.string("a>b").data(), as: UTF8.self) == "\"a\\u003eb\"")
        #expect(String(decoding: try JSON.string("a&b").data(), as: UTF8.self) == "\"a\\u0026b\"")
        #expect(String(decoding: try JSON.string("\u{2028}").data(), as: UTF8.self) == "\"\\u2028\"")
        #expect(String(decoding: try JSON.string("\u{2029}").data(), as: UTF8.self) == "\"\\u2029\"")
        #expect(String(decoding: try JSON.string("a/b").data(), as: UTF8.self) == "\"a/b\"")
        #expect(String(decoding: try JSON.string("\u{0B}").data(), as: UTF8.self) == "\"\\u000b\"")
    }

    /// `Wire.object` (ordered fields) must stay byte-identical too.
    @Test func orderedObjectMatchesReference() throws {
        let fields: [(String, JSON)] = [("protocolVersion", .int(3)), ("datasetID", .string("d<1>")),
                                        ("nested", .object(["b": .bool(true), "a": .array([.int(1)])])), ("null", .null)]
        let reference = try referenceObject(fields), produced = try Wire.object(fields)
        #expect(reference == produced)
    }
    // MARK: - R4 gzip wire contract (client side of the server's bundle limits)

    /// The bundle wire is one deterministic gzip member inside one multipart
    /// file part. Byte identity matters because the server hashes the
    /// *decompressed* bytes as `bundleSHA256`, while the wire bytes may legally
    /// differ (compression level, gzip header timestamp) for the same JSON.
    @Test func gzipBundleWireIsOneDeterministicMember() throws {
        let raw = Data(String(repeating: "{\"synthetic\":true}\n", count: 512).utf8)
        let first = try Wire.gzip(raw)
        let second = try Wire.gzip(raw)
        #expect(first == second, "gzip must be deterministic so a retry reuses the sealed artifact")
        // Fixed header: magic, deflate, no flags, MTIME 0, XFL 0, OS 255.
        #expect(Array(first.prefix(10)) == [0x1F, 0x8B, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xFF])
        // Single member: the trailing 8 bytes are CRC-32 and ISIZE.
        #expect(first.count > 18)
        let isize = Array(first.suffix(4)).enumerated().reduce(0) { $0 | (Int($1.element) << (8 * $1.offset)) }
        #expect(isize == raw.count, "ISIZE must equal the decompressed length")
        // Exactly one member: no second gzip magic after the first member's header.
        #expect(!first.dropFirst(10).dropLast(8).starts(with: Data([0x1F, 0x8B])))

        let wire = WorkoutBundleAssembler.multipart(bundle: first, boundary: "rai.wire.test", compressed: true)
        let text = String(decoding: wire.prefix(160), as: UTF8.self)
        #expect(text.hasPrefix("--rai.wire.test\r\n"))
        #expect(text.contains("name=\"file\""))
        #expect(text.contains("filename=\"workout.json.gz\""))
        #expect(text.contains("Content-Type: application/gzip"))
        #expect(!text.contains("filename=\"workout.json\""))
        #expect(!text.contains("application/json"))
        #expect(wire.range(of: Data([0x1F, 0x8B])) != nil)
        #expect(String(decoding: wire.suffix(24), as: UTF8.self).hasSuffix("--rai.wire.test--\r\n"))
    }

    /// The client mirrors the server's compressed-bundle limits so an
    /// over-limit bundle fails locally instead of being downgraded to plaintext.
    @Test func gzipBundleLimitsMirrorTheServerContract() throws {
        #expect(Wire.GzipBundle.maxDecodedBytes == 32 << 20)
        #expect(Wire.GzipBundle.maxCompressedBytes == 8 << 20)
        #expect(Wire.GzipBundle.ratioCap == 100)
        #expect(Wire.GzipBundle.ratioSlack == 64 << 10)

        try Wire.GzipBundle.validate(decoded: 32 << 20, compressed: 8 << 20)
        #expect(throws: SyncFailure.localState("bundle_too_large")) {
            try Wire.GzipBundle.validate(decoded: (32 << 20) + 1, compressed: 1)
        }
        #expect(throws: SyncFailure.localState("bundle_compressed_too_large")) {
            try Wire.GzipBundle.validate(decoded: 1000, compressed: (8 << 20) + 1)
        }
        // Ratio: exactly `100 * compressed + 64 KiB` is still legal.
        let compressed = 1024
        try Wire.GzipBundle.validate(decoded: 100 * compressed + (64 << 10), compressed: compressed)
        #expect(throws: SyncFailure.localState("bundle_compression_ratio_exceeded")) {
            try Wire.GzipBundle.validate(decoded: 100 * compressed + (64 << 10) + 1, compressed: compressed)
        }
    }
}
