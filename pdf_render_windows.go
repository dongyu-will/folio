//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/egoist/mygo"
)

var (
	windowsPDFForeground = &pdfDaemonSlot{idleTTL: 60 * time.Second}
	windowsPDFBackground = &pdfDaemonSlot{idleTTL: 60 * time.Second}
)

func closePDFRendererCachePlatform() {
	windowsPDFForeground.close()
	windowsPDFBackground.close()
}

func renderPDFPlatform(ctx context.Context, path string, page, maxSide int, output string, background bool) (pdfRenderResult, error) {
	// 1. Prefer bundled WinRT C# helper daemon when packaged alongside the app.
	if helper := windowsPDFHelperPath(); helper != "" {
		slot := windowsPDFForeground
		if background {
			slot = windowsPDFBackground
		}
		if res, err := slot.render(ctx, helper, path, page, maxSide, output); err == nil && res.Error == "" {
			return res, nil
		}
	}

	// 2. Fallback to fast installed CLI PDF tools if present on PATH.
	if mutool, err := exec.LookPath("mutool"); err == nil {
		if res, err := renderWithMutool(ctx, mutool, path, page, maxSide, output); err == nil {
			return res, nil
		}
	}
	if pdftoppm, err := exec.LookPath("pdftoppm"); err == nil {
		if res, err := renderWithPdftoppm(ctx, pdftoppm, path, page, maxSide, output); err == nil {
			return res, nil
		}
	}

	// 3. Built-in zero-dependency Windows 10/11 WinRT PdfDocument PowerShell daemon.
	ps, err := exec.LookPath("powershell")
	if err != nil {
		return pdfRenderResult{}, errors.New("找不到可用 PDF 渲染器（请确认系统支持 Windows WinRT PDF 或安装 MuPDF/Poppler）")
	}
	slot := windowsPDFForeground
	if background {
		slot = windowsPDFBackground
	}
	return slot.render(ctx, ps, path, page, maxSide, output,
		"-NoProfile", "-NonInteractive", "-NoLogo", "-WindowStyle", "Hidden", "-Command", winrtPowerShellDaemonScript)
}

func searchPDFPlatform(ctx context.Context, docPath, query string, maxHits int) ([]pdfSearchHit, bool) {
	helper := windowsPDFHelperPath()
	if helper == "" {
		return nil, false
	}
	hits, err := windowsPDFBackground.search(ctx, helper, docPath, query, maxHits)
	if err != nil {
		return nil, false
	}
	return hits, true
}

func windowsPDFHelperPath() string {
	var dirs []string
	if root, err := mygo.App.Path(mygo.PathResources); err == nil {
		dirs = append(dirs, root, filepath.Join(root, "windows"))
	}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	dirs = append(dirs, filepath.Join("resources", "windows"))
	for _, dir := range dirs {
		p := filepath.Join(dir, "mgnote-pdf.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func hiddenWindowsCmd(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

func renderWithMutool(ctx context.Context, mutool, path string, page, maxSide int, output string) (pdfRenderResult, error) {
	infoOut, _ := hiddenWindowsCmd(ctx, mutool, "info", "-M", path).CombinedOutput()
	count, w, h := parseMutoolInfoOutput(string(infoOut))
	dpi := 144
	if maxPts := max(w, h); maxPts > 0 && maxSide > 0 {
		dpi = clampInt(int(float32(maxSide)*72.0/maxPts+0.5), 48, 300)
	}
	out, err := hiddenWindowsCmd(ctx, mutool, "draw", "-o", output, "-r", strconv.Itoa(dpi), path, strconv.Itoa(page+1)).CombinedOutput()
	if err != nil {
		return pdfRenderResult{}, errors.New(strings.TrimSpace(string(out)))
	}
	if count == 0 {
		count = page + 1
	}
	return pdfRenderResult{Count: count, Width: w, Height: h}, nil
}

func renderWithPdftoppm(ctx context.Context, pdftoppm, path string, page, maxSide int, output string) (pdfRenderResult, error) {
	var count int
	var w, h float32
	if pdfinfo, err := exec.LookPath("pdfinfo"); err == nil {
		if infoOut, err := hiddenWindowsCmd(ctx, pdfinfo, path).CombinedOutput(); err == nil {
			count, w, h = parsePDFInfoOutput(string(infoOut))
		}
	}
	prefix := strings.TrimSuffix(output, filepath.Ext(output))
	out, err := hiddenWindowsCmd(ctx, pdftoppm,
		"-png", "-singlefile",
		"-f", strconv.Itoa(page+1),
		"-l", strconv.Itoa(page+1),
		"-scale-to", strconv.Itoa(max(120, maxSide)),
		path, prefix,
	).CombinedOutput()
	if err != nil {
		return pdfRenderResult{}, errors.New(strings.TrimSpace(string(out)))
	}
	generated := prefix + ".png"
	if generated != output {
		_ = os.Rename(generated, output)
	}
	if count == 0 {
		count = page + 1
	}
	return pdfRenderResult{Count: count, Width: w, Height: h}, nil
}

const winrtPowerShellDaemonScript = `
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Runtime.WindowsRuntime
$null = [Windows.Data.Pdf.PdfDocument, Windows.Data.Pdf, ContentType=WindowsRuntime]
$null = [Windows.Storage.StorageFile, Windows.Storage, ContentType=WindowsRuntime]
$null = [Windows.Storage.Streams.InMemoryRandomAccessStream, Windows.Storage.Streams, ContentType=WindowsRuntime]

$asTaskGeneric = ([System.WindowsRuntimeSystemExtensions].GetMethods() | Where-Object {
  $_.Name -eq 'AsTask' -and $_.GetParameters().Count -eq 1 -and $_.GetParameters()[0].ParameterType.Name -eq 'IAsyncOperation` + "`1" + `'
})[0]
$asTaskAction = ([System.WindowsRuntimeSystemExtensions].GetMethods() | Where-Object {
  $_.Name -eq 'AsTask' -and $_.GetParameters().Count -eq 1 -and $_.GetParameters()[0].ParameterType.Name -eq 'IAsyncAction'
})[0]

function Await-Async($op, [type]$resType) {
  $m = $asTaskGeneric.MakeGenericMethod($resType)
  $t = $m.Invoke($null, @($op))
  return $t.GetAwaiter().GetResult()
}
function Await-Action($op) {
  $t = $asTaskAction.Invoke($null, @($op))
  $t.GetAwaiter().GetResult()
}

$cachedPath = $null
$cachedDoc = $null
$cachedAttrs = $null

while (($line = [Console]::In.ReadLine()) -ne $null) {
  if ([string]::IsNullOrWhiteSpace($line)) { continue }
  try {
    $req = ConvertFrom-Json $line
    $fi = Get-Item -LiteralPath $req.path
    $attrKey = "$($fi.Length):$($fi.LastWriteTimeUtc.Ticks)"
    if ($cachedDoc -eq $null -or $cachedPath -ne $req.path -or $cachedAttrs -ne $attrKey) {
      $sf = Await-Async ([Windows.Storage.StorageFile]::GetFileFromPathAsync($req.path)) ([Windows.Storage.StorageFile])
      $cachedDoc = Await-Async ([Windows.Data.Pdf.PdfDocument]::LoadFromFileAsync($sf)) ([Windows.Data.Pdf.PdfDocument])
      $cachedPath = $req.path
      $cachedAttrs = $attrKey
    }
    $count = [int]$cachedDoc.PageCount
    $idx = [uint32]$req.page
    if ($idx -ge $cachedDoc.PageCount) {
      throw "页码超出范围"
    }
    $p = $cachedDoc.GetPage($idx)
    try {
      $sz = $p.Size
      $w = [double]$sz.Width
      $h = [double]$sz.Height
      $scale = [double]$req.maxSide / [Math]::Max($w, $h)
      $dw = [uint32][Math]::Max(1, [Math]::Ceiling($w * $scale))
      $dh = [uint32][Math]::Max(1, [Math]::Ceiling($h * $scale))
      $mem = New-Object Windows.Storage.Streams.InMemoryRandomAccessStream
      try {
        $opts = New-Object Windows.Data.Pdf.PdfPageRenderOptions
        $opts.DestinationWidth = $dw
        $opts.DestinationHeight = $dh
        Await-Action ($p.RenderToStreamAsync($mem, $opts))
        $mem.Seek(0)
        $bytes = New-Object byte[] $mem.Size
        $stream = [System.IO.WindowsRuntimeStreamExtensions]::AsStream($mem)
        [void]$stream.Read($bytes, 0, $bytes.Length)
        [System.IO.File]::WriteAllBytes($req.output, $bytes)
      } finally {
        $mem.Dispose()
      }
      $resp = @{ count = $count; width = $w; height = $h } | ConvertTo-Json -Compress
      [Console]::Out.WriteLine($resp)
    } finally {
      $p.Dispose()
    }
  } catch {
    $resp = @{ error = $_.Exception.Message } | ConvertTo-Json -Compress
    [Console]::Out.WriteLine($resp)
  }
  [Console]::Out.Flush()
}
`
