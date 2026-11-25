//go:build !windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/dongyu-will/folio/internal/androidopen"
	"github.com/egoist/mygo"
)

var (
	darwinPDFForeground = &pdfDaemonSlot{idleTTL: 60 * time.Second}
	darwinPDFBackground = &pdfDaemonSlot{idleTTL: 60 * time.Second}
)

func closePDFRendererCachePlatform() {
	darwinPDFForeground.close()
	darwinPDFBackground.close()
	if runtime.GOOS == "android" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = mygo.Native(androidopen.PluginID).Call(ctx, "closePDFCache", nil, nil)
		cancel()
	}
}

func renderPDFPlatform(ctx context.Context, path string, page, maxSide int, output string, background bool) (pdfRenderResult, error) {
	switch runtime.GOOS {
	case "android":
		var result pdfRenderResult
		args := map[string]any{"path": path, "output": output, "page": page, "maxSide": maxSide, "background": background}
		err := mygo.Native(androidopen.PluginID).Call(ctx, "renderPDF", args, &result)
		return result, err
	case "darwin":
		renderer := pdfRendererPath()
		if renderer == "" {
			return pdfRenderResult{}, errors.New("找不到 PDFKit 渲染器")
		}
		slot := darwinPDFForeground
		if background {
			slot = darwinPDFBackground
		}
		return slot.render(ctx, renderer, path, page, maxSide, output)
	default:
		return pdfRenderResult{}, errors.New("PDF 渲染支持 Android、macOS 和 Windows")
	}
}

func searchPDFPlatform(ctx context.Context, docPath, query string, maxHits int) ([]pdfSearchHit, bool) {
	if runtime.GOOS != "darwin" {
		return nil, false
	}
	renderer := pdfRendererPath()
	if renderer == "" {
		return nil, false
	}
	hits, err := darwinPDFBackground.search(ctx, renderer, docPath, query, maxHits)
	if err != nil {
		return nil, false
	}
	return hits, true
}

func pdfRendererPath() string {
	var dirs []string
	if root, err := mygo.App.Path(mygo.PathResources); err == nil {
		dirs = append(dirs, root, filepath.Join(root, "darwin"))
	}
	dirs = append(dirs, filepath.Join("resources", "darwin"))
	for _, dir := range dirs {
		path := filepath.Join(dir, "mgnote-pdf")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}
