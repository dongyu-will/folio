import AppKit
import PencilKit

let arguments = CommandLine.arguments
let width = Double(arguments[1]) ?? 0
let height = Double(arguments[2]) ?? 0
let target = Double(arguments[3]) ?? 0

do {
    let data = FileHandle.standardInput.readDataToEndOfFile()
    let drawing = try PKDrawing(data: data)
    let rect = width > 0 && height > 0
        ? CGRect(x: 0, y: 0, width: width, height: height)
        : drawing.bounds.insetBy(dx: -4, dy: -4).integral
    let scale = target > 0 ? target / rect.width : 2
    guard !drawing.strokes.isEmpty, !rect.isEmpty, !rect.isInfinite,
          scale.isFinite, scale > 0,
          rect.width * rect.height * scale * scale <= 28_000_000 else { exit(1) }
    let image = drawing.image(from: rect, scale: scale)
    guard let tiff = image.tiffRepresentation,
          let bitmap = NSBitmapImageRep(data: tiff),
          let png = bitmap.representation(using: .png, properties: [:]) else { exit(1) }
    try FileHandle.standardOutput.write(contentsOf: png)
} catch {
    exit(1)
}
