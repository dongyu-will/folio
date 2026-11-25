import AppKit
import Foundation
import PDFKit

private struct RenderRequest: Decodable {
    let action: String?
    let path: String
    let page: Int?
    let maxSide: Double?
    let output: String?
    let query: String?
    let maxHits: Int?
}

private struct SearchHit: Encodable {
    let page: Int
    let snippet: String
}

private struct RenderResponse: Encodable {
    var count: Int?
    var width: Double?
    var height: Double?
    var hits: [SearchHit]?
    var error: String?
}

private final class CachedPDF {
    private var cachedPath: String?
    private var cachedMTime: Date?
    private var cachedSize: NSNumber?
    private var cachedDoc: PDFDocument?

    func document(for path: String) -> PDFDocument? {
        let attrs = try? FileManager.default.attributesOfItem(atPath: path)
        let mtime = attrs?[.modificationDate] as? Date
        let size = attrs?[.size] as? NSNumber
        if let doc = cachedDoc,
           cachedPath == path,
           cachedMTime == mtime,
           cachedSize == size {
            return doc
        }
        guard let doc = PDFDocument(url: URL(fileURLWithPath: path)) else {
            cachedPath = nil
            cachedMTime = nil
            cachedSize = nil
            cachedDoc = nil
            return nil
        }
        cachedPath = path
        cachedMTime = mtime
        cachedSize = size
        cachedDoc = doc
        return doc
    }
}

private func renderPage(doc: PDFDocument, index: Int, maxSide: Double, output: String) throws -> RenderResponse {
    guard !doc.isLocked, index >= 0, index < doc.pageCount, let page = doc.page(at: index) else {
        throw CocoaError(.fileReadCorruptFile)
    }
    let bounds = page.bounds(for: .cropBox)
    guard bounds.width > 0, bounds.height > 0 else {
        throw CocoaError(.fileReadCorruptFile)
    }
    let scale = maxSide / max(bounds.width, bounds.height)
    let size = NSSize(width: max(1, ceil(bounds.width * scale)), height: max(1, ceil(bounds.height * scale)))
    try autoreleasepool {
        let thumbnail = page.thumbnail(of: size, for: .cropBox)
        guard let cg = thumbnail.cgImage(forProposedRect: nil, context: nil, hints: nil) else {
            throw CocoaError(.fileReadCorruptFile)
        }
        let bitmap = NSBitmapImageRep(cgImage: cg)
        guard let png = bitmap.representation(using: .png, properties: [:]) else {
            throw CocoaError(.fileWriteUnknown)
        }
        try png.write(to: URL(fileURLWithPath: output), options: .atomic)
    }
    return RenderResponse(count: doc.pageCount, width: Double(bounds.width), height: Double(bounds.height), hits: nil, error: nil)
}

private func buildSnippet(pageText: String, selectionText: String) -> String {
    let cleanedSel = selectionText.replacingOccurrences(of: "\\s+", with: " ", options: .regularExpression).trimmingCharacters(in: .whitespacesAndNewlines)
    let cleanedPage = pageText.replacingOccurrences(of: "\\s+", with: " ", options: .regularExpression)
    if !cleanedSel.isEmpty, let range = cleanedPage.range(of: cleanedSel, options: .caseInsensitive) {
        let start = cleanedPage.index(range.lowerBound, offsetBy: -28, limitedBy: cleanedPage.startIndex) ?? cleanedPage.startIndex
        let end = cleanedPage.index(range.upperBound, offsetBy: 44, limitedBy: cleanedPage.endIndex) ?? cleanedPage.endIndex
        var snippet = String(cleanedPage[start..<end]).trimmingCharacters(in: .whitespacesAndNewlines)
        if start > cleanedPage.startIndex {
            snippet = "…" + snippet
        }
        if end < cleanedPage.endIndex {
            snippet += "…"
        }
        return snippet
    }
    return String(cleanedSel.prefix(96))
}

private func searchDocument(doc: PDFDocument, query: String, maxHits: Int) -> RenderResponse {
    let trimmed = query.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !trimmed.isEmpty, !doc.isLocked else {
        return RenderResponse(count: doc.pageCount, width: nil, height: nil, hits: [], error: nil)
    }
    let limit = max(1, min(500, maxHits))
    let selections = doc.findString(trimmed, withOptions: .caseInsensitive)
    var hits: [SearchHit] = []
    hits.reserveCapacity(min(limit, selections.count))
    for sel in selections {
        guard let page = sel.pages.first else {
            continue
        }
        let pageIdx = doc.index(for: page)
        if pageIdx == NSNotFound || pageIdx < 0 {
            continue
        }
        let snippet = buildSnippet(pageText: page.string ?? "", selectionText: sel.string ?? trimmed)
        hits.append(SearchHit(page: pageIdx, snippet: snippet))
        if hits.count >= limit {
            break
        }
    }
    return RenderResponse(count: doc.pageCount, width: nil, height: nil, hits: hits, error: nil)
}

private func runOneShot(args: [String]) throws {
    guard args.count == 5, let index = Int(args[2]), let maxSide = Double(args[3]),
          let doc = PDFDocument(url: URL(fileURLWithPath: args[1])) else {
        throw CocoaError(.fileReadCorruptFile)
    }
    let res = try renderPage(doc: doc, index: index, maxSide: maxSide, output: args[4])
    let data = try JSONEncoder().encode(res)
    if let json = String(data: data, encoding: .utf8) {
        print(json)
    }
}

private func runDaemon() {
    let cache = CachedPDF()
    let decoder = JSONDecoder()
    let encoder = JSONEncoder()
    while let line = readLine(strippingNewline: true) {
        if line.isEmpty {
            continue
        }
        var res = RenderResponse()
        autoreleasepool {
            guard let raw = line.data(using: .utf8),
                  let req = try? decoder.decode(RenderRequest.self, from: raw) else {
                res.error = "无效的 PDF 请求"
                return
            }
            guard let doc = cache.document(for: req.path) else {
                res.error = "无法读取 PDF 文件"
                return
            }
            if req.action == "search" {
                res = searchDocument(doc: doc, query: req.query ?? "", maxHits: req.maxHits ?? 200)
                return
            }
            guard let page = req.page, let maxSide = req.maxSide, let output = req.output, !output.isEmpty else {
                res.error = "无效的 PDF 渲染参数"
                return
            }
            do {
                res = try renderPage(doc: doc, index: page, maxSide: maxSide, output: output)
            } catch {
                res.error = error.localizedDescription
            }
        }
        if let out = try? encoder.encode(res), let str = String(data: out, encoding: .utf8) {
            print(str)
        } else {
            print("{\"error\":\"无法编码 PDF 响应\"}")
        }
        fflush(stdout)
    }
}

let args = CommandLine.arguments
if args.count == 2 && args[1] == "--daemon" {
    runDaemon()
} else {
    do {
        try runOneShot(args: args)
    } catch {
        FileHandle.standardError.write(Data((error.localizedDescription + "\n").utf8))
        exit(1)
    }
}
