package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/dongyu-will/folio/internal/androidopen"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type pageRef struct {
	document string
	page     int
}

type renderedPDFPage struct {
	light, night *ui.Bitmap
	raster       *image.RGBA
	count        int
	err          error
}

func renderPDF(path string, page, maxSide int, background bool) (renderedPDFPage, error) {
	var result struct {
		Count int
		Error string
	}
	f, err := os.CreateTemp("", "mgnote-pdf-*.png")
	if err != nil {
		return renderedPDFPage{}, err
	}
	output := f.Name()
	defer os.Remove(output)
	if err := f.Close(); err != nil {
		return renderedPDFPage{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	switch runtime.GOOS {
	case "android":
		args := map[string]any{"path": path, "output": output, "page": page, "maxSide": maxSide, "background": background}
		err = mygo.Native(androidopen.PluginID).Call(ctx, "renderPDF", args, &result)
	case "darwin":
		renderer := pdfRendererPath()
		if renderer == "" {
			return renderedPDFPage{}, errors.New("找不到 PDFKit 渲染器")
		}
		data, runErr := exec.CommandContext(ctx, renderer, path, strconv.Itoa(page), strconv.Itoa(maxSide), output).Output()
		if runErr != nil {
			err = runErr
		} else {
			err = json.Unmarshal(data, &result)
		}
	default:
		err = errors.New("PDF 渲染支持 Android 和 macOS")
	}
	if err != nil {
		return renderedPDFPage{}, fmt.Errorf("无法渲染 PDF：%w", err)
	}
	if result.Error != "" {
		return renderedPDFPage{}, errors.New(result.Error)
	}
	file, err := os.Open(output)
	if err != nil {
		return renderedPDFPage{}, err
	}
	defer file.Close()
	src, _, err := image.Decode(file)
	if err != nil {
		return renderedPDFPage{}, err
	}
	light := image.NewRGBA(src.Bounds())
	draw.Draw(light, light.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(light, light.Bounds(), src, src.Bounds().Min, draw.Over)
	return renderedPDFPage{light: ui.NewBitmap(light), raster: light, count: result.Count}, nil
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

// Shift paper and neutral text into a dark palette while preserving hue
// and increasing the luminance of dark colored content for readability.
func nightPDF(src *image.RGBA) *image.RGBA {
	dst := image.NewRGBA(src.Bounds())
	for i := 0; i < len(src.Pix); i += 4 {
		r, g, b := float32(src.Pix[i]), float32(src.Pix[i+1]), float32(src.Pix[i+2])
		y := (r*54 + g*183 + b*19) / 256
		c := max(r, g, b) - min(r, g, b)
		weight := clamp((c-14)/78, 0, 1)
		yDark := 24 + (255-y)*.79
		shift := (yDark-y)*(1-weight) + max(0, 130-y)*weight
		dst.Pix[i] = uint8(clamp(r+shift, 0, 255))
		dst.Pix[i+1] = uint8(clamp(g+shift, 0, 255))
		dst.Pix[i+2] = uint8(clamp(b+shift, 0, 255))
		dst.Pix[i+3] = 255
	}
	return dst
}
