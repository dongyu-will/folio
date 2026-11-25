package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

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
	side         int
	failedSide   int
	aspect       float32
	err          error
}

func (p renderedPDFPage) aspectRatio() float32 {
	if p.aspect > 0 {
		return p.aspect
	}
	if p.light != nil {
		w, h := p.light.Size()
		return float32(h) / float32(max(1, w))
	}
	return 1.414
}

type pdfRenderResult struct {
	Count  int            `json:"count"`
	Error  string         `json:"error"`
	Width  float32        `json:"width"`
	Height float32        `json:"height"`
	Hits   []pdfSearchHit `json:"hits,omitempty"`
}

type pdfDaemonRequest struct {
	Action  string `json:"action,omitempty"`
	Path    string `json:"path"`
	Page    int    `json:"page,omitempty"`
	MaxSide int    `json:"maxSide,omitempty"`
	Output  string `json:"output,omitempty"`
	Query   string `json:"query,omitempty"`
	MaxHits int    `json:"maxHits,omitempty"`
}

type darwinPDFSlot = pdfDaemonSlot

type pdfDaemonSlot struct {
	mu           sync.Mutex
	rendererPath string
	rendererArgs string
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	reader       *bufio.Reader
	done         chan struct{}
	timer        *time.Timer
	gen          uint64
	idleTTL      time.Duration
}

func (s *pdfDaemonSlot) stopLocked() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.gen++
	if s.stdin != nil {
		_ = s.stdin.Close()
		s.stdin = nil
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	done := s.done
	s.cmd = nil
	s.reader = nil
	s.done = nil
	s.rendererPath = ""
	s.rendererArgs = ""
	if done != nil {
		<-done
	}
}

func (s *pdfDaemonSlot) close() {
	s.mu.Lock()
	s.stopLocked()
	s.mu.Unlock()
}

func (s *pdfDaemonSlot) startLocked(renderer string, args []string) error {
	if len(args) == 0 {
		args = []string{"--daemon"}
	}
	argsKey := fmt.Sprintf("%q", args)
	if s.cmd != nil && s.rendererPath == renderer && s.rendererArgs == argsKey {
		select {
		case <-s.done:
			s.stopLocked()
		default:
			return nil
		}
	} else if s.cmd != nil {
		s.stopLocked()
	}

	cmd := exec.Command(renderer, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return err
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	s.rendererPath = renderer
	s.rendererArgs = argsKey
	s.cmd = cmd
	s.stdin = stdin
	s.reader = bufio.NewReader(stdout)
	s.done = done
	return nil
}

func (s *pdfDaemonSlot) scheduleIdleLocked() {
	if s.timer != nil {
		s.timer.Stop()
	}
	ttl := s.idleTTL
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	gen := s.gen
	s.timer = time.AfterFunc(ttl, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.gen == gen {
			s.stopLocked()
		}
	})
}

func (s *pdfDaemonSlot) request(ctx context.Context, renderer string, args []string, req pdfDaemonRequest) (pdfRenderResult, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return pdfRenderResult{}, err
	}
	payload = append(payload, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}

	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return pdfRenderResult{}, err
		}
		if err := s.startLocked(renderer, args); err != nil {
			break
		}
		if _, err := s.stdin.Write(payload); err != nil {
			s.stopLocked()
			continue
		}
		type lineResult struct {
			line []byte
			err  error
		}
		ch := make(chan lineResult, 1)
		reader := s.reader
		go func() {
			line, readErr := reader.ReadBytes('\n')
			ch <- lineResult{line: line, err: readErr}
		}()
		select {
		case res := <-ch:
			if res.err != nil {
				s.stopLocked()
				continue
			}
			var out pdfRenderResult
			if err := json.Unmarshal(bytes.TrimSpace(res.line), &out); err != nil {
				s.stopLocked()
				return pdfRenderResult{}, err
			}
			s.scheduleIdleLocked()
			return out, nil
		case <-ctx.Done():
			s.stopLocked()
			<-ch
			return pdfRenderResult{}, ctx.Err()
		}
	}

	return pdfRenderResult{}, errors.New("PDF 渲染进程无响应")
}

func (s *pdfDaemonSlot) render(ctx context.Context, renderer, path string, page, maxSide int, output string, args ...string) (pdfRenderResult, error) {
	res, err := s.request(ctx, renderer, args, pdfDaemonRequest{
		Path:    path,
		Page:    page,
		MaxSide: maxSide,
		Output:  output,
	})
	if err == nil {
		return res, nil
	}
	if len(args) == 0 && ctx.Err() == nil {
		return renderPDFOneShot(ctx, renderer, path, page, maxSide, output)
	}
	return pdfRenderResult{}, err
}

func (s *pdfDaemonSlot) search(ctx context.Context, renderer, path, query string, maxHits int, args ...string) ([]pdfSearchHit, error) {
	res, err := s.request(ctx, renderer, args, pdfDaemonRequest{
		Action:  "search",
		Path:    path,
		Query:   query,
		MaxHits: maxHits,
	})
	if err != nil {
		return nil, err
	}
	if res.Error != "" {
		return nil, errors.New(res.Error)
	}
	return res.Hits, nil
}

func renderPDFOneShot(ctx context.Context, renderer, path string, page, maxSide int, output string) (pdfRenderResult, error) {
	data, err := exec.CommandContext(ctx, renderer, path, strconv.Itoa(page), strconv.Itoa(maxSide), output).Output()
	if err != nil {
		return pdfRenderResult{}, err
	}
	var out pdfRenderResult
	if err := json.Unmarshal(data, &out); err != nil {
		return pdfRenderResult{}, err
	}
	return out, nil
}

func closePDFRendererCache() {
	closePDFRendererCachePlatform()
}

func renderPDF(path string, page, maxSide int, background bool) (renderedPDFPage, error) {
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
	result, err := renderPDFPlatform(ctx, path, page, maxSide, output, background)
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
	var aspect float32
	if result.Width > 0 && result.Height > 0 {
		aspect = result.Height / result.Width
	}
	return renderedPDFPage{light: ui.NewBitmap(light), raster: light, count: result.Count, side: maxSide, aspect: aspect}, nil
}

var (
	pdfInfoPagesRe    = regexp.MustCompile(`(?m)^Pages:\s*(\d+)`)
	pdfInfoPageSizeRe = regexp.MustCompile(`(?m)^Page size:\s*([0-9.]+)\s*x\s*([0-9.]+)`)
	mutoolPagesRe     = regexp.MustCompile(`(?i)Pages:\s*(\d+)`)
	mutoolBoxRe       = regexp.MustCompile(`\[\s*([0-9.-]+)\s+([0-9.-]+)\s+([0-9.-]+)\s+([0-9.-]+)\s*\]`)
)

func parsePDFInfoOutput(raw string) (count int, width, height float32) {
	if m := pdfInfoPagesRe.FindStringSubmatch(raw); len(m) == 2 {
		count, _ = strconv.Atoi(m[1])
	}
	if m := pdfInfoPageSizeRe.FindStringSubmatch(raw); len(m) == 3 {
		w, _ := strconv.ParseFloat(m[1], 32)
		h, _ := strconv.ParseFloat(m[2], 32)
		width, height = float32(w), float32(h)
	}
	return count, width, height
}

func parseMutoolInfoOutput(raw string) (count int, width, height float32) {
	if m := mutoolPagesRe.FindStringSubmatch(raw); len(m) == 2 {
		count, _ = strconv.Atoi(m[1])
	}
	if m := mutoolBoxRe.FindStringSubmatch(raw); len(m) == 5 {
		x0, _ := strconv.ParseFloat(m[1], 32)
		y0, _ := strconv.ParseFloat(m[2], 32)
		x1, _ := strconv.ParseFloat(m[3], 32)
		y1, _ := strconv.ParseFloat(m[4], 32)
		if w := float32(x1 - x0); w > 0 {
			width = w
		}
		if h := float32(y1 - y0); h > 0 {
			height = h
		}
	}
	if count == 0 {
		// Fall back to counting <page lines or Page lines when Pages: header is absent.
		for _, line := range strings.Split(raw, "\n") {
			if strings.Contains(line, "<page ") || strings.HasPrefix(strings.TrimSpace(line), "Page ") {
				count++
			}
		}
	}
	return count, width, height
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
