// Standalone Windows WinRT PDF renderer daemon matching Folio's JSON-lines
// protocol (`--daemon` stdin/stdout and `<path> <page> <maxSide> <output>` one-shot).
// Compile on Windows with scripts/build-windows-pdf.ps1 to produce mgnote-pdf.exe.

using System;
using System.IO;
using System.Text.Json;
using System.Text.Json.Serialization;
using System.Threading.Tasks;
using Windows.Data.Pdf;
using Windows.Storage;
using Windows.Storage.Streams;

internal sealed class RenderRequest
{
    [JsonPropertyName("action")] public string? Action { get; set; }
    [JsonPropertyName("path")] public string Path { get; set; } = "";
    [JsonPropertyName("page")] public int Page { get; set; }
    [JsonPropertyName("maxSide")] public double MaxSide { get; set; }
    [JsonPropertyName("output")] public string Output { get; set; } = "";
}

internal sealed class RenderResponse
{
    [JsonPropertyName("count")] public int? Count { get; set; }
    [JsonPropertyName("width")] public double? Width { get; set; }
    [JsonPropertyName("height")] public double? Height { get; set; }
    [JsonPropertyName("error")] public string? Error { get; set; }
}

internal static class Program
{
    private static string? _cachedPath;
    private static string? _cachedStamp;
    private static PdfDocument? _cachedDoc;

    private static async Task<PdfDocument> LoadDocAsync(string path)
    {
        var info = new FileInfo(path);
        var stamp = $"{info.Length}:{info.LastWriteTimeUtc.Ticks}";
        if (_cachedDoc != null && _cachedPath == path && _cachedStamp == stamp)
        {
            return _cachedDoc;
        }
        var sf = await StorageFile.GetFileFromPathAsync(path).AsTask();
        _cachedDoc = await PdfDocument.LoadFromFileAsync(sf).AsTask();
        _cachedPath = path;
        _cachedStamp = stamp;
        return _cachedDoc;
    }

    private static async Task<RenderResponse> RenderAsync(string path, int pageIdx, double maxSide, string output)
    {
        var doc = await LoadDocAsync(path);
        if (pageIdx < 0 || pageIdx >= doc.PageCount)
        {
            throw new ArgumentOutOfRangeException(nameof(pageIdx), "页码超出范围");
        }
        using var page = doc.GetPage((uint)pageIdx);
        var w = page.Size.Width;
        var h = page.Size.Height;
        var scale = maxSide / Math.Max(w, h);
        var opts = new PdfPageRenderOptions
        {
            DestinationWidth = (uint)Math.Max(1, Math.Ceiling(w * scale)),
            DestinationHeight = (uint)Math.Max(1, Math.Ceiling(h * scale)),
        };
        using var mem = new InMemoryRandomAccessStream();
        await page.RenderToStreamAsync(mem, opts).AsTask();
        mem.Seek(0);
        await using var netStream = mem.AsStreamForRead();
        var tmp = output + ".tmp";
        await using (var fs = File.Create(tmp))
        {
            await netStream.CopyToAsync(fs);
        }
        File.Move(tmp, output, overwrite: true);
        return new RenderResponse { Count = (int)doc.PageCount, Width = w, Height = h };
    }

    public static async Task<int> Main(string[] args)
    {
        if (args.Length == 1 && args[0] == "--daemon")
        {
            string? line;
            while ((line = Console.In.ReadLine()) != null)
            {
                if (string.IsNullOrWhiteSpace(line)) continue;
                RenderResponse resp;
                try
                {
                    var req = JsonSerializer.Deserialize<RenderRequest>(line);
                    if (req == null || string.IsNullOrEmpty(req.Path))
                    {
                        resp = new RenderResponse { Error = "无效的 PDF 请求" };
                    }
                    else
                    {
                        resp = await RenderAsync(req.Path, req.Page, req.MaxSide, req.Output);
                    }
                }
                catch (Exception ex)
                {
                    resp = new RenderResponse { Error = ex.Message };
                }
                Console.Out.WriteLine(JsonSerializer.Serialize(resp));
                Console.Out.Flush();
            }
            return 0;
        }
        if (args.Length != 4)
        {
            Console.Error.WriteLine("Usage: mgnote-pdf [--daemon | <path> <page> <maxSide> <output>]");
            return 1;
        }
        var res = await RenderAsync(args[0], int.Parse(args[1]), double.Parse(args[2]), args[3]);
        Console.Out.WriteLine(JsonSerializer.Serialize(res));
        return 0;
    }
}
