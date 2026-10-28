// macOS-only visual regression utility; consumes screenshots, never edits them.
// swift Tools/CompareSnapshots.swift <baseline-directory> <candidate-directory>
import AppKit
import Foundation

let args = CommandLine.arguments
let names = ["Home", "Training", "SessionDetail", "Analysis", "Execution"]
func pixels(_ url: URL) throws -> (Int, Int, [UInt8]) {
  guard let data = try? Data(contentsOf: url), let rep = NSBitmapImageRep(data: data),
    let image = rep.cgImage
  else { throw NSError(domain: "snapshot_missing", code: 1) }
  let w = image.width
  let h = image.height
  var bytes = [UInt8](repeating: 0, count: w * h * 4)
  let ok = bytes.withUnsafeMutableBytes { buffer in
    guard
      let context = CGContext(
        data: buffer.baseAddress, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w * 4,
        space: CGColorSpaceCreateDeviceRGB(),
        bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)
    else { return false }
    context.draw(image, in: CGRect(x: 0, y: 0, width: w, height: h))
    return true
  }
  guard ok else { throw NSError(domain: "snapshot_decode", code: 2) }
  return (w, h, bytes)
}
guard args.count == 3 else {
  fputs("usage: CompareSnapshots baseline-dir candidate-dir\n", stderr)
  exit(2)
}
var failures = 0
for name in names {
  do {
    let a = try pixels(URL(fileURLWithPath: args[1]).appendingPathComponent(name + ".png"))
    let b = try pixels(URL(fileURLWithPath: args[2]).appendingPathComponent(name + ".png"))
    guard a.0 == b.0 && a.1 == b.1 else { throw NSError(domain: "snapshot_dimensions", code: 3) }
    var changed = 0
    var total = 0
    var error = 0
    for i in stride(from: 0, to: a.2.count, by: 16) {
      let delta = (0..<3).map { abs(Int(a.2[i + $0]) - Int(b.2[i + $0])) }
      if delta.max()! > 32 { changed += 1 }
      error += delta.reduce(0, +)
      total += 1
    }
    let fraction = Double(changed) / Double(total)
    let mean = Double(error) / Double(total * 3)
    let pass = fraction <= 0.10 && mean <= 12
    print("\(name): \(pass ? "PASS":"FAIL") changed=\(fraction) mean_channel_error=\(mean)")
    if !pass { failures += 1 }
  } catch {
    print("\(name): FAIL missing, invalid or different dimensions")
    failures += 1
  }
}
exit(failures == 0 ? 0 : 1)
