import AppKit
import PDFKit

func render() throws {
    let args = CommandLine.arguments
    guard args.count == 5, let index = Int(args[2]), let maxSide = Double(args[3]),
          let doc = PDFDocument(url: URL(fileURLWithPath: args[1])),
          !doc.isLocked, let page = doc.page(at: index) else {
        throw CocoaError(.fileReadCorruptFile)
    }
    let bounds = page.bounds(for: .cropBox)
    guard bounds.width > 0, bounds.height > 0 else { throw CocoaError(.fileReadCorruptFile) }
    let scale = maxSide / max(bounds.width, bounds.height)
    let size = NSSize(width: max(1, ceil(bounds.width * scale)), height: max(1, ceil(bounds.height * scale)))
    let thumbnail = page.thumbnail(of: size, for: .cropBox)
    guard let cg = thumbnail.cgImage(forProposedRect: nil, context: nil, hints: nil) else {
        throw CocoaError(.fileReadCorruptFile)
    }
    let bitmap = NSBitmapImageRep(cgImage: cg)
    guard let png = bitmap.representation(using: .png, properties: [:]) else {
        throw CocoaError(.fileWriteUnknown)
    }
    try png.write(to: URL(fileURLWithPath: args[4]), options: .atomic)
    print("{\"count\":\(doc.pageCount),\"width\":\(bounds.width),\"height\":\(bounds.height)}")
}

do {
    try render()
} catch {
    FileHandle.standardError.write(Data((error.localizedDescription + "\n").utf8))
    exit(1)
}
