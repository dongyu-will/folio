package notebook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/html"
	"howett.net/plist"
)

const currentMediaVersion = 5

func mediaVersion(string) int { return currentMediaVersion }

type HighlightRect struct {
	Page int     `json:"page"`
	X    float32 `json:"x"`
	Y    float32 `json:"y"`
	W    float32 `json:"w"`
	H    float32 `json:"h"`
}

type TextSpan struct {
	Text      string  `json:"text"`
	Color     string  `json:"color,omitempty"`
	Bg        string  `json:"bg,omitempty"`
	Bold      bool    `json:"bold,omitempty"`
	Italic    bool    `json:"italic,omitempty"`
	Underline bool    `json:"underline,omitempty"`
	FontSize  float32 `json:"fontSize,omitempty"`
}

type ContentBlock struct {
	Text  string     `json:"text,omitempty"`
	Image string     `json:"image,omitempty"`
	Spans []TextSpan `json:"spans,omitempty"`
}

var mediaName = regexp.MustCompile(`^[a-f0-9]{64}\.(png|jpeg|gif)$`)

func (n Note) BlocksCoverBody(body string) bool {
	if n.BodyEdited || len(n.Blocks) == 0 {
		return false
	}
	if strings.TrimSpace(body) == "" {
		return true
	}
	var text strings.Builder
	for _, block := range n.Blocks {
		text.WriteString(block.Text)
		text.WriteByte('\n')
	}
	return strings.Join(strings.Fields(text.String()), " ") == strings.Join(strings.Fields(body), " ")
}

type mediaSink interface {
	save(name string, data []byte) error
}

type inkCacheKey struct {
	hash   [32]byte
	width  int
	height int
	pixels int
}

type inkCacheEntry struct {
	ready chan struct{}
	png   []byte
	rem   atomic.Int32
}

type mediaRefPlan struct {
	needed  map[string]bool
	needRaw map[string]bool
}

func newMediaRefPlan() *mediaRefPlan {
	return &mediaRefPlan{
		needed:  make(map[string]bool),
		needRaw: make(map[string]bool),
	}
}

func (p *mediaRefPlan) addPicture(pic map[string]any) {
	if pic == nil {
		return
	}
	hasDrawing := false
	switch v := pic["drawing"].(type) {
	case string:
		if v != "" {
			hasDrawing = true
			p.needed[v] = true
			p.needRaw[v] = true
		}
	case []byte:
		if len(v) > 0 {
			hasDrawing = true
		}
	}
	for _, field := range []string{"paint", "img", "pic"} {
		if key, ok := pic[field].(string); ok && key != "" {
			p.needed[key] = true
			if hasDrawing {
				p.needRaw[key] = true
			}
		}
	}
}

func (p *mediaRefPlan) addParsedNote(clip map[string]any, blocks []any) {
	if clip != nil {
		p.addPicture(clip)
	}
	for _, value := range blocks {
		block, ok := value.(map[string]any)
		if !ok {
			continue
		}
		switch block["type"] {
		case "LinkNote":
			if qpic, ok := block["q_hpic"].(map[string]any); ok {
				p.addPicture(qpic)
			}
		case "PaintNote", "PicNote", "ImageNote", "MergedImageNote", "SketchNote":
			p.addPicture(block)
		}
	}
}

type mediaImport struct {
	sources   map[string][]byte
	savedKeys map[string]string
	ink       string
	sink      mediaSink

	filesMu sync.Mutex
	files   map[string][]byte

	inkCounts map[inkCacheKey]int
	inkMu     sync.Mutex
	inkCache  map[inkCacheKey]*inkCacheEntry

	largeCanvasSem chan struct{}

	errMu    sync.Mutex
	firstErr error
}

const largeCanvasPixels = 2_000_000

func newMediaImport(ink string, sink mediaSink) *mediaImport {
	largeCap := 2
	if runtime.GOOS == "android" || runtime.GOOS == "ios" {
		largeCap = 1
	}
	m := &mediaImport{
		sources:        make(map[string][]byte),
		savedKeys:      make(map[string]string),
		ink:            ink,
		sink:           sink,
		largeCanvasSem: make(chan struct{}, largeCap),
	}
	if sink == nil {
		m.files = make(map[string][]byte)
	}
	return m
}

func (m *mediaImport) acquireLargeCanvas(pixels int64) func() {
	if m.largeCanvasSem == nil || pixels <= largeCanvasPixels {
		return func() {}
	}
	m.largeCanvasSem <- struct{}{}
	return func() { <-m.largeCanvasSem }
}

func (m *mediaImport) recordErr(err error) {
	if err == nil {
		return
	}
	m.errMu.Lock()
	if m.firstErr == nil {
		m.firstErr = err
	}
	m.errMu.Unlock()
}

func (m *mediaImport) err() error {
	m.errMu.Lock()
	err := m.firstErr
	m.errMu.Unlock()
	return err
}

// releaseTransient drops raw source blobs and ink memoization tables once note
// media attachment finishes so peak memory falls before PDF/shard writes.
func (m *mediaImport) releaseTransient() {
	m.sources = nil
	m.savedKeys = nil
	m.inkCounts = nil
	m.inkMu.Lock()
	m.inkCache = nil
	m.inkMu.Unlock()
}

func readMediaFiltered(ctx context.Context, db *sql.DB, m *mediaImport, plan *mediaRefPlan) error {
	if plan != nil && len(plan.needed) == 0 {
		return nil
	}
	var present bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name='ZMEDIA')").Scan(&present); err != nil || !present {
		return err
	}

	nestedKeys := make(map[string]bool)
	storeRaw := func(key string, data []byte) {
		if _, seen := m.sources[key]; seen {
			return
		}
		unarchived := unarchive(data)
		if unpacked, ok := unarchived.([]byte); ok {
			m.sources[key] = unpacked
			return
		}
		m.sources[key] = data
		if wrapped, ok := unarchived.(map[string]any); ok {
			for _, field := range []string{"drawing2", "drawing1"} {
				if nested, ok := wrapped[field].(string); ok && nested != "" {
					nestedKeys[nested] = true
				}
			}
		}
	}

	if err := func() error {
		rows, err := db.QueryContext(ctx, "SELECT ZMD5, ZDATA FROM ZMEDIA WHERE ZMD5 IS NOT NULL AND ZDATA IS NOT NULL")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			var data []byte
			if err := rows.Scan(&key, &data); err != nil {
				return err
			}
			if plan != nil && !plan.needed[key] && !nestedKeys[key] {
				continue
			}
			needRaw := plan == nil || plan.needRaw[key] || nestedKeys[key]
			if !needRaw && m.sink != nil {
				if _, seen := m.savedKeys[key]; seen {
					continue
				}
				if unpacked, ok := unarchive(data).([]byte); ok {
					data = unpacked
				}
				_, ext, ok := imageConfig(data)
				if !ok {
					m.savedKeys[key] = ""
					continue
				}
				name := fmt.Sprintf("%x.%s", sha256.Sum256(data), ext)
				if err := m.sink.save(name, data); err != nil {
					return err
				}
				m.savedKeys[key] = name
				continue
			}
			storeRaw(key, data)
		}
		return rows.Err()
	}(); err != nil {
		return err
	}

	// Fetch any nested drawing1/drawing2 keys referenced by wrapped bplist
	// entries that appeared earlier in ZMEDIA than their wrapper row.
	const batchSize = 500
	for depth := 0; depth < 3 && len(nestedKeys) > 0; depth++ {
		var missing []string
		for k := range nestedKeys {
			if _, have := m.sources[k]; !have {
				missing = append(missing, k)
			}
		}
		clear(nestedKeys)
		if len(missing) == 0 {
			break
		}
		for start := 0; start < len(missing); start += batchSize {
			batch := missing[start:min(start+batchSize, len(missing))]
			if err := func() error {
				placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
				args := make([]any, len(batch))
				for i, k := range batch {
					args[i] = k
				}
				rows, err := db.QueryContext(ctx, "SELECT ZMD5, ZDATA FROM ZMEDIA WHERE ZMD5 IN ("+placeholders+") AND ZDATA IS NOT NULL", args...)
				if err != nil {
					return err
				}
				defer rows.Close()
				for rows.Next() {
					var key string
					var data []byte
					if err := rows.Scan(&key, &data); err != nil {
						return err
					}
					storeRaw(key, data)
				}
				return rows.Err()
			}(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *mediaImport) countInkRefs(notes []rawSourceNote) {
	counts := make(map[inkCacheKey]int)
	hashByKey := make(map[string][32]byte)
	hashOf := func(val any, raw []byte) [32]byte {
		if k, ok := val.(string); ok && k != "" {
			if h, exists := hashByKey[k]; exists {
				return h
			}
			h := sha256.Sum256(raw)
			hashByKey[k] = h
			return h
		}
		return sha256.Sum256(raw)
	}
	visitPic := func(pic map[string]any) {
		if pic == nil {
			return
		}
		drawingVal := pic["drawing"]
		drawing := m.source(drawingVal)
		if len(drawing) == 0 {
			return
		}
		paint := m.picturePaint(pic)
		cfg, _, _ := imageConfig(paint)
		width, height := pictureInkSize(pic, cfg)
		pixels := cfg.Width
		if wrapped, ok := unarchive(drawing).(map[string]any); ok {
			for _, field := range []string{"drawing2", "drawing1"} {
				if cand := m.source(wrapped[field]); len(cand) > 0 {
					key := inkCacheKey{hash: hashOf(wrapped[field], cand), width: width, height: height, pixels: pixels}
					counts[key]++
				}
			}
			return
		}
		key := inkCacheKey{hash: hashOf(drawingVal, drawing), width: width, height: height, pixels: pixels}
		counts[key]++
	}
	for i := range notes {
		visitPic(notes[i].clip)
		for _, value := range notes[i].blocks {
			block, ok := value.(map[string]any)
			if !ok {
				continue
			}
			switch block["type"] {
			case "LinkNote":
				if qpic, ok := block["q_hpic"].(map[string]any); ok {
					visitPic(qpic)
				}
			case "PaintNote", "PicNote", "ImageNote", "MergedImageNote", "SketchNote":
				visitPic(block)
			}
		}
	}
	m.inkCounts = counts
}

// The property list can contain Foundation object references. Interpret only
// the value types used by the export and never instantiate archived classes.
func unarchive(data []byte) any {
	if !bytes.HasPrefix(data, []byte("bplist00")) {
		return nil
	}
	var archive struct {
		Objects []any                `plist:"$objects"`
		Top     map[string]plist.UID `plist:"$top"`
	}
	if _, err := plist.Unmarshal(data, &archive); err != nil {
		return nil
	}
	budget := 20000
	var decode func(any, int) any
	decode = func(value any, depth int) any {
		budget--
		if depth > 24 || budget < 0 {
			return nil
		}
		switch v := value.(type) {
		case plist.UID:
			if v > 0 && uint64(v) < uint64(len(archive.Objects)) {
				return decode(archive.Objects[v], depth+1)
			}
		case map[string]any:
			for _, field := range []string{"NS.string", "NS.data", "NS.sizeval", "NS.rectval", "NS.pointval"} {
				if raw, ok := v[field]; ok {
					return decode(raw, depth+1)
				}
			}
			items, _ := v["NS.objects"].([]any)
			if keys, ok := v["NS.keys"].([]any); ok {
				values := make(map[string]any)
				for i := 0; i < min(len(keys), len(items)) && budget > 0; i++ {
					if key, ok := decode(keys[i], depth+1).(string); ok {
						values[key] = decode(items[i], depth+1)
					}
				}
				return values
			}
			values := make([]any, 0, len(items))
			for _, raw := range items {
				if budget <= 0 {
					break
				}
				values = append(values, decode(raw, depth+1))
			}
			return values
		case string, []byte, int, int64, uint64, float32, float64, bool:
			return v
		}
		return nil
	}
	return decode(archive.Top["root"], 0)
}

func (m *mediaImport) attach(n *Note, clipData, noteData []byte) bool {
	clip, _ := unarchive(clipData).(map[string]any)
	blocks, _ := unarchive(noteData).([]any)
	return m.attachParsed(n, clip, blocks)
}

func (m *mediaImport) attachParsed(n *Note, clip map[string]any, blocks []any) bool {
	return attachNoteContent(n, clip, blocks, m.savePicture)
}

func attachNoteContent(n *Note, clip map[string]any, blocks []any, picture func(map[string]any) string) bool {
	if clip != nil {
		n.Clip = picture(clip)
		extractHighlightRects(n, clip)
	}
	var content []ContentBlock
	var hasImage, hasRich, sawDummy, sawSketch, sawOther bool
	addLink := func(raw string) {
		for _, id := range extractNoteLinks(raw, n.ID) {
			if !slices.Contains(n.Links, id) {
				n.Links = append(n.Links, id)
			}
		}
	}
	for _, value := range blocks {
		block, ok := value.(map[string]any)
		if !ok {
			continue
		}
		switch block["type"] {
		case "DummyNote":
			sawDummy = true
		case "TextNote":
			sawOther = true
			if text, _ := block["text"].(string); strings.TrimSpace(text) != "" {
				content = append(content, ContentBlock{Text: text})
				addLink(text)
			}
		case "HtmlNote":
			sawOther = true
			if rawHTML, _ := block["html"].(string); rawHTML != "" {
				addLink(rawHTML)
			}
			if text, spans := parseHTMLNote(block); text != "" {
				addLink(text)
				if spansHaveStyling(spans) {
					hasRich = true
					content = append(content, ContentBlock{Text: text, Spans: spans})
				} else {
					content = append(content, ContentBlock{Text: text})
				}
			}
		case "LinkNote":
			sawOther = true
			if noteID, _ := block["noteid"].(string); strings.TrimSpace(noteID) != "" {
				if id := normalizeNoteLinkID(noteID); id != "" && id != n.ID && !slices.Contains(n.Links, id) {
					n.Links = append(n.Links, id)
				}
			}
			if text, _ := block["text"].(string); text != "" {
				addLink(text)
			}
			var mergedImg string
			if qpic, ok := block["q_hpic"].(map[string]any); ok {
				mergedImg = picture(qpic)
				extractHighlightRects(n, qpic)
				if mergedImg != "" {
					content = append(content, ContentBlock{Image: mergedImg})
					hasImage = true
				}
			}
			if qtext, _ := block["q_htext"].(string); strings.TrimSpace(qtext) != "" {
				addLink(qtext)
				if mergedImg == "" {
					content = append(content, ContentBlock{Text: strings.TrimSpace(qtext)})
				}
			}
		case "PaintNote", "PicNote", "ImageNote", "MergedImageNote":
			sawOther = true
			if file := picture(block); file != "" {
				content = append(content, ContentBlock{Image: file})
				hasImage = true
			}
		case "SketchNote":
			sawSketch = true
			if file := picture(block); file != "" {
				content = append(content, ContentBlock{Image: file})
				hasImage = true
			}
		default:
			sawOther = true
		}
	}
	if hasImage || hasRich || len(content) > 1 {
		n.Blocks = content
	}
	if strings.TrimSpace(n.Body) == "" && len(content) > 0 {
		var parts []string
		for _, b := range content {
			if strings.TrimSpace(b.Text) != "" {
				parts = append(parts, b.Text)
			}
		}
		if len(parts) > 0 {
			n.Body = strings.Join(parts, "\n\n")
		}
	}
	addLink(n.Body)
	addLink(n.Excerpt)
	appendTags(n, extractHashtags(n.Title)...)
	appendTags(n, extractHashtags(n.Body)...)
	appendTags(n, extractHashtags(n.Excerpt)...)
	for _, b := range content {
		if b.Text != "" {
			appendTags(n, extractHashtags(b.Text)...)
		}
	}
	return (sawDummy || sawSketch) && !sawOther
}

var (
	htmlTagHint   = regexp.MustCompile(`(?i)</?(?:html|head|body|p|div|span|br|ul|ol|li|b|strong|i|em|u|font|mark|h[1-6]|table|tr|td|style|meta)\b|<!doctype\s+html`)
	cssRuleRE     = regexp.MustCompile(`(?i)([a-z0-9_.*,\s-]+?)\s*\{([^}]*)\}`)
	hexColorRE    = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)
	rgbColorRE    = regexp.MustCompile(`(?i)^rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)`)
	cssFontSizeRE = regexp.MustCompile(`(?i)\b(\d+(?:\.\d+)?)\s*(px|pt)\b`)
	nsRectRE      = regexp.MustCompile(`^\s*\{\s*\{\s*([-+]?\d+(?:\.\d+)?(?:[eE][-+]?\d+)?)\s*,\s*([-+]?\d+(?:\.\d+)?(?:[eE][-+]?\d+)?)\s*\}\s*,\s*\{\s*([-+]?\d+(?:\.\d+)?(?:[eE][-+]?\d+)?)\s*,\s*([-+]?\d+(?:\.\d+)?(?:[eE][-+]?\d+)?)\s*\}\s*\}\s*$`)
	mnNoteLinkRE  = regexp.MustCompile(`(?i)marginnote\d*app://note/([A-Za-z0-9-]+)`)
	hashtagRE     = regexp.MustCompile(`(?:^|[\s\p{Z}])#([\p{L}\p{N}_][\p{L}\p{N}_/-]*)`)
)

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case uint64:
		if n <= math.MaxInt {
			return int(n), true
		}
	case float64:
		if !math.IsNaN(n) && !math.IsInf(n, 0) {
			return int(math.Round(n)), true
		}
	case float32:
		if !math.IsNaN(float64(n)) && !math.IsInf(float64(n), 0) {
			return int(math.Round(float64(n))), true
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return i, true
		}
	}
	return 0, false
}

func parseNSRect(raw any) (float32, float32, float32, float32, bool) {
	s, ok := raw.(string)
	if !ok {
		return 0, 0, 0, 0, false
	}
	m := nsRectRE.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, 0, 0, false
	}
	x, err1 := strconv.ParseFloat(m[1], 32)
	y, err2 := strconv.ParseFloat(m[2], 32)
	w, err3 := strconv.ParseFloat(m[3], 32)
	h, err4 := strconv.ParseFloat(m[4], 32)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || w <= 0 || h <= 0 {
		return 0, 0, 0, 0, false
	}
	return float32(x), float32(y), float32(w), float32(h), true
}

func extractHighlightRects(n *Note, clip map[string]any) {
	if selList, ok := clip["selLst"].([]any); ok {
		for _, entry := range selList {
			m, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			pageNo, hasPage := asInt(m["pageNo"])
			if !hasPage || pageNo <= 0 {
				continue
			}
			if n.Page == nil || *n.Page <= 0 {
				p := int64(pageNo)
				n.Page = &p
			}
			if x, y, w, h, ok := parseNSRect(m["rect"]); ok {
				n.HighlightRects = append(n.HighlightRects, HighlightRect{
					Page: pageNo, X: x, Y: y, W: w, H: h,
				})
			}
		}
	}
	if rawMasks, ok := clip["maskRects"].([]any); ok {
		defaultPage := 0
		if n.Page != nil && *n.Page > 0 {
			defaultPage = int(*n.Page)
		}
		for _, entry := range rawMasks {
			switch v := entry.(type) {
			case map[string]any:
				pageNo := defaultPage
				if p, ok := asInt(v["pageNo"]); ok && p > 0 {
					pageNo = p
				}
				if x, y, w, h, ok := parseNSRect(v["rect"]); ok {
					n.MaskRects = append(n.MaskRects, HighlightRect{
						Page: pageNo, X: x, Y: y, W: w, H: h,
					})
				}
			case string:
				if x, y, w, h, ok := parseNSRect(v); ok {
					n.MaskRects = append(n.MaskRects, HighlightRect{
						Page: defaultPage, X: x, Y: y, W: w, H: h,
					})
				}
			}
		}
	}
}

func normalizeNoteLinkID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if m := mnNoteLinkRE.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	if strings.ContainsAny(raw, " \t\r\n/") {
		return ""
	}
	return raw
}

func extractNoteLinks(raw, selfID string) []string {
	if !strings.Contains(strings.ToLower(raw), "marginnote") {
		return nil
	}
	var out []string
	for _, m := range mnNoteLinkRE.FindAllStringSubmatch(raw, -1) {
		id := strings.TrimSpace(m[1])
		if id != "" && id != selfID && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func extractHashtags(raw string) []string {
	if !strings.Contains(raw, "#") {
		return nil
	}
	var out []string
	for _, m := range hashtagRE.FindAllStringSubmatch(raw, -1) {
		tag := strings.TrimRight(m[1], "-/")
		if tag != "" && !slices.Contains(out, tag) {
			out = append(out, tag)
		}
	}
	return out
}

func appendTags(n *Note, tags ...string) {
	for _, tag := range tags {
		tag = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(tag), "#"))
		if tag != "" && !slices.Contains(n.Tags, tag) {
			n.Tags = append(n.Tags, tag)
		}
	}
}

func spansHaveStyling(spans []TextSpan) bool {
	for _, s := range spans {
		if s.Color != "" || s.Bg != "" || s.Bold || s.Italic || s.Underline || s.FontSize > 0 {
			return true
		}
	}
	return false
}

func cleanHTMLNote(block map[string]any) string {
	text, _ := block["text"].(string)
	if strings.TrimSpace(text) == "" {
		text, _ = block["html"].(string)
	}
	if strings.TrimSpace(text) == "" {
		return ""
	}
	if htmlTagHint.MatchString(text) {
		return stripHTML(text)
	}
	return strings.TrimSpace(text)
}

func parseHTMLNote(block map[string]any) (string, []TextSpan) {
	rawHTML, _ := block["html"].(string)
	if strings.TrimSpace(rawHTML) != "" && htmlTagHint.MatchString(rawHTML) {
		return parseHTMLRich(rawHTML)
	}
	rawText, _ := block["text"].(string)
	if strings.TrimSpace(rawText) != "" && htmlTagHint.MatchString(rawText) {
		return parseHTMLRich(rawText)
	}
	plain := cleanHTMLNote(block)
	if plain == "" {
		return "", nil
	}
	return plain, nil
}

type cssProps struct {
	hasColor     bool
	color        string
	hasBg        bool
	bg           string
	hasBold      bool
	bold         bool
	hasItalic    bool
	italic       bool
	hasUnderline bool
	underline    bool
	hasFontSize  bool
	fontSize     float32
}

func (p *cssProps) merge(o cssProps) {
	if o.hasColor {
		p.hasColor, p.color = true, o.color
	}
	if o.hasBg {
		p.hasBg, p.bg = true, o.bg
	}
	if o.hasBold {
		p.hasBold, p.bold = true, o.bold
	}
	if o.hasItalic {
		p.hasItalic, p.italic = true, o.italic
	}
	if o.hasUnderline {
		p.hasUnderline, p.underline = true, o.underline
	}
	if o.hasFontSize {
		p.hasFontSize, p.fontSize = true, o.fontSize
	}
}

func parseCSSFontSize(raw string) (float32, bool) {
	m := cssFontSizeRE.FindStringSubmatch(raw)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	if strings.EqualFold(m[2], "pt") {
		v = v * 4.0 / 3.0
	}
	if v < 6 || v > 120 {
		return 0, false
	}
	return float32(v), true
}

func normalizeCSSColor(raw string, isBg bool) (string, bool) {
	v := strings.TrimSpace(strings.ToLower(raw))
	v = strings.TrimSuffix(v, "!important")
	v = strings.TrimSpace(v)
	if v == "" || v == "inherit" || v == "initial" || v == "transparent" || v == "none" {
		return "", false
	}
	if m := hexColorRE.FindStringSubmatch(v); m != nil {
		h := m[1]
		if len(h) == 3 {
			h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
		}
		hexStr := "#" + strings.ToLower(h)
		// Default black foreground / white paper background inherit from the active theme.
		if !isBg && (hexStr == "#000000" || hexStr == "#111111" || hexStr == "#1c1c1e") {
			return "", true
		}
		if isBg && (hexStr == "#ffffff" || hexStr == "#fafafa" || hexStr == "#f5f5f7") {
			return "", true
		}
		return hexStr, true
	}
	if m := rgbColorRE.FindStringSubmatch(v); m != nil {
		r, _ := strconv.Atoi(m[1])
		g, _ := strconv.Atoi(m[2])
		b, _ := strconv.Atoi(m[3])
		hexStr := fmt.Sprintf("#%02x%02x%02x", min(255, max(0, r)), min(255, max(0, g)), min(255, max(0, b)))
		if !isBg && r < 24 && g < 24 && b < 24 {
			return "", true
		}
		if isBg && r > 245 && g > 245 && b > 245 {
			return "", true
		}
		return hexStr, true
	}
	switch v {
	case "red":
		return "#ff0000", true
	case "blue":
		return "#2000ff", true
	case "green":
		return "#16a34a", true
	case "orange":
		return "#ff6000", true
	case "black":
		return "", !isBg
	case "white":
		return "", isBg
	}
	return "", false
}

func parseCSSDecls(decls string) cssProps {
	var p cssProps
	for _, part := range strings.Split(decls, ";") {
		k, v, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		key := strings.TrimSpace(strings.ToLower(k))
		val := strings.TrimSpace(v)
		low := strings.ToLower(val)
		switch key {
		case "color":
			if col, valid := normalizeCSSColor(val, false); valid {
				p.hasColor, p.color = true, col
			}
		case "background-color", "background":
			if col, valid := normalizeCSSColor(val, true); valid {
				p.hasBg, p.bg = true, col
			}
		case "font-weight":
			if strings.Contains(low, "bold") {
				p.hasBold, p.bold = true, true
			} else if strings.Contains(low, "normal") {
				p.hasBold, p.bold = true, false
			} else if n, err := strconv.Atoi(strings.Fields(low)[0]); err == nil {
				p.hasBold, p.bold = true, n >= 600
			}
		case "font-style":
			if strings.Contains(low, "italic") || strings.Contains(low, "oblique") {
				p.hasItalic, p.italic = true, true
			} else if strings.Contains(low, "normal") {
				p.hasItalic, p.italic = true, false
			}
		case "font", "font-family":
			if strings.Contains(low, "semibold") || strings.Contains(low, "bold") || strings.Contains(low, "heavy") {
				p.hasBold, p.bold = true, true
			} else if key == "font" && !p.hasBold {
				// Cocoa HTML resets font shorthand (e.g. "18.0px 'PingFang SC'") back to regular weight.
				p.hasBold, p.bold = true, false
			}
			if strings.Contains(low, "italic") || strings.Contains(low, "oblique") {
				p.hasItalic, p.italic = true, true
			}
			if key == "font" {
				if sz, ok := parseCSSFontSize(low); ok {
					p.hasFontSize, p.fontSize = true, sz
				}
			}
		case "font-size":
			if sz, ok := parseCSSFontSize(low); ok {
				p.hasFontSize, p.fontSize = true, sz
			}
		case "text-decoration", "text-decoration-line":
			if strings.Contains(low, "underline") {
				p.hasUnderline, p.underline = true, true
			} else if strings.Contains(low, "none") {
				p.hasUnderline, p.underline = true, false
			}
		}
	}
	return p
}

func parseStylesheet(css string) map[string]cssProps {
	rules := make(map[string]cssProps)
	for _, m := range cssRuleRE.FindAllStringSubmatch(css, -1) {
		props := parseCSSDecls(m[2])
		for _, sel := range strings.Split(m[1], ",") {
			s := strings.TrimSpace(strings.ToLower(sel))
			if s == "" {
				continue
			}
			cur := rules[s]
			cur.merge(props)
			rules[s] = cur
		}
	}
	return rules
}

func parseHTMLRich(raw string) (string, []TextSpan) {
	// Pass 1: collect all <style> blocks.
	z1 := html.NewTokenizer(strings.NewReader(raw))
	var cssBuf strings.Builder
	inStyle := false
	for {
		tt := z1.Next()
		if tt == html.ErrorToken {
			break
		}
		switch tt {
		case html.StartTagToken:
			name, _ := z1.TagName()
			if strings.EqualFold(string(name), "style") {
				inStyle = true
			}
		case html.EndTagToken:
			name, _ := z1.TagName()
			if strings.EqualFold(string(name), "style") {
				inStyle = false
			}
		case html.TextToken:
			if inStyle {
				cssBuf.Write(z1.Text())
				cssBuf.WriteByte('\n')
			}
		}
	}
	rules := parseStylesheet(cssBuf.String())

	// Pass 2: walk body HTML with an inherited style stack.
	type stackFrame struct {
		tag   string
		style TextSpan
	}
	stack := []stackFrame{{tag: "root"}}
	var spans []TextSpan
	skipDepth := 0
	paraHasText := false

	appendSpan := func(text string, st TextSpan) {
		if text == "" {
			return
		}
		if len(spans) > 0 {
			last := &spans[len(spans)-1]
			if last.Color == st.Color && last.Bg == st.Bg && last.Bold == st.Bold &&
				last.Italic == st.Italic && last.Underline == st.Underline && last.FontSize == st.FontSize {
				last.Text += text
				return
			}
		}
		st.Text = text
		spans = append(spans, st)
	}

	z := html.NewTokenizer(strings.NewReader(raw))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			nameBytes, hasAttr := z.TagName()
			tag := strings.ToLower(string(nameBytes))
			var classAttr, styleAttr, colorAttr string
			for hasAttr {
				k, v, more := z.TagAttr()
				hasAttr = more
				switch strings.ToLower(string(k)) {
				case "class":
					classAttr = string(v)
				case "style":
					styleAttr = string(v)
				case "color":
					colorAttr = string(v)
				}
			}
			switch tag {
			case "head", "style", "script", "title":
				if tt == html.StartTagToken {
					skipDepth++
				}
				continue
			case "br":
				if skipDepth == 0 {
					appendSpan("\n", TextSpan{})
					paraHasText = true
				}
				continue
			}
			if skipDepth > 0 {
				continue
			}
			if tag == "p" || tag == "div" || tag == "li" || tag == "h1" || tag == "h2" || tag == "h3" || tag == "h4" || tag == "h5" || tag == "h6" || tag == "blockquote" {
				paraHasText = false
			}
			if tag == "li" {
				if len(spans) > 0 && !strings.HasSuffix(spans[len(spans)-1].Text, "\n") {
					appendSpan("\n", TextSpan{})
				}
				appendSpan("• ", stack[len(stack)-1].style)
			}

			cur := stack[len(stack)-1].style
			var delta cssProps
			switch tag {
			case "b", "strong", "h1", "h2", "h3", "h4", "h5", "h6":
				delta.hasBold, delta.bold = true, true
			case "i", "em":
				delta.hasItalic, delta.italic = true, true
			case "u":
				delta.hasUnderline, delta.underline = true, true
			case "mark":
				delta.hasBg, delta.bg = true, "#fef08a"
			}
			if colorAttr != "" {
				if col, valid := normalizeCSSColor(colorAttr, false); valid {
					delta.hasColor, delta.color = true, col
				}
			}
			if r, ok := rules[tag]; ok {
				delta.merge(r)
			}
			for _, cls := range strings.Fields(strings.ToLower(classAttr)) {
				if r, ok := rules["."+cls]; ok {
					delta.merge(r)
				}
				if r, ok := rules[tag+"."+cls]; ok {
					delta.merge(r)
				}
			}
			if styleAttr != "" {
				delta.merge(parseCSSDecls(styleAttr))
			}
			if delta.hasColor {
				cur.Color = delta.color
			}
			if delta.hasBg {
				cur.Bg = delta.bg
			}
			if delta.hasBold {
				cur.Bold = delta.bold
			}
			if delta.hasItalic {
				cur.Italic = delta.italic
			}
			if delta.hasUnderline {
				cur.Underline = delta.underline
			}
			if delta.hasFontSize {
				cur.FontSize = delta.fontSize
			}
			if tt == html.StartTagToken {
				stack = append(stack, stackFrame{tag: tag, style: cur})
			}

		case html.EndTagToken:
			nameBytes, _ := z.TagName()
			tag := strings.ToLower(string(nameBytes))
			switch tag {
			case "head", "style", "script", "title":
				if skipDepth > 0 {
					skipDepth--
				}
				continue
			}
			if skipDepth > 0 {
				continue
			}
			switch tag {
			case "p", "div", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6", "blockquote":
				// Cocoa HTML uses <p><br></p> for blank lines; avoid doubling into two newlines.
				if !(len(spans) > 0 && strings.HasSuffix(spans[len(spans)-1].Text, "\n\n")) {
					if !paraHasText && len(spans) > 0 && strings.HasSuffix(spans[len(spans)-1].Text, "\n") {
						appendSpan("\n", TextSpan{})
					} else if paraHasText && !(len(spans) > 0 && strings.HasSuffix(spans[len(spans)-1].Text, "\n")) {
						appendSpan("\n", TextSpan{})
					}
				}
				paraHasText = false
			}
			for i := len(stack) - 1; i > 0; i-- {
				if stack[i].tag == tag {
					stack = stack[:i]
					break
				}
			}

		case html.TextToken:
			if skipDepth > 0 {
				continue
			}
			chunk := strings.ReplaceAll(string(z.Text()), "\u00a0", " ")
			chunk = strings.ReplaceAll(chunk, "\r\n", "\n")
			chunk = strings.ReplaceAll(chunk, "\r", "\n")
			// Ignore pure formatting newlines between HTML tags outside paragraphs.
			if strings.Trim(chunk, "\n\t ") == "" {
				if !paraHasText {
					continue
				}
				if strings.Contains(chunk, "\n") {
					continue
				}
			}
			if strings.TrimSpace(chunk) != "" {
				paraHasText = true
			}
			appendSpan(chunk, stack[len(stack)-1].style)
		}
	}

	spans = normalizeRichSpans(spans)
	var plain strings.Builder
	for _, s := range spans {
		plain.WriteString(s.Text)
	}
	return plain.String(), spans
}

// normalizeRichSpans trims leading/trailing blank lines and collapses 3+ consecutive
// newlines to a single blank line while preserving every span's styling.
func normalizeRichSpans(in []TextSpan) []TextSpan {
	if len(in) == 0 {
		return nil
	}
	// Flatten into line-safe chunks preserving styles, collapsing >2 newlines.
	var out []TextSpan
	nlRun := 0
	started := false
	push := func(text string, st TextSpan) {
		if text == "" {
			return
		}
		if len(out) > 0 {
			last := &out[len(out)-1]
			if last.Color == st.Color && last.Bg == st.Bg && last.Bold == st.Bold &&
				last.Italic == st.Italic && last.Underline == st.Underline && last.FontSize == st.FontSize {
				last.Text += text
				return
			}
		}
		st.Text = text
		out = append(out, st)
	}
	for _, sp := range in {
		var buf strings.Builder
		for _, r := range sp.Text {
			if r == '\n' {
				if !started {
					continue
				}
				nlRun++
				if nlRun <= 2 {
					buf.WriteRune('\n')
				}
			} else {
				if !started && (r == ' ' || r == '\t') {
					continue
				}
				started = true
				nlRun = 0
				buf.WriteRune(r)
			}
		}
		push(buf.String(), sp)
	}
	// Trim trailing whitespace/newlines cleanly.
	for len(out) > 0 {
		trimmed := strings.TrimRight(out[len(out)-1].Text, " \t\n")
		if trimmed == "" {
			out = out[:len(out)-1]
			continue
		}
		out[len(out)-1].Text = trimmed
		break
	}
	return out
}

func stripHTML(raw string) string {
	z := html.NewTokenizer(strings.NewReader(raw))
	var out strings.Builder
	skipDepth := 0
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return normalizeLines(out.String())
		case html.StartTagToken, html.SelfClosingTagToken:
			nameBytes, _ := z.TagName()
			tag := strings.ToLower(string(nameBytes))
			switch tag {
			case "head", "style", "script", "title":
				if tt == html.StartTagToken {
					skipDepth++
				}
			case "br":
				if skipDepth == 0 {
					out.WriteByte('\n')
				}
			case "li":
				if skipDepth == 0 {
					if out.Len() > 0 && !strings.HasSuffix(out.String(), "\n") {
						out.WriteByte('\n')
					}
					out.WriteString("• ")
				}
			}
		case html.EndTagToken:
			nameBytes, _ := z.TagName()
			tag := strings.ToLower(string(nameBytes))
			switch tag {
			case "head", "style", "script", "title":
				if skipDepth > 0 {
					skipDepth--
				}
			case "p", "div", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6", "blockquote":
				if skipDepth == 0 {
					out.WriteByte('\n')
				}
			}
		case html.TextToken:
			if skipDepth == 0 {
				chunk := strings.ReplaceAll(string(z.Text()), "\u00a0", " ")
				out.WriteString(chunk)
			}
		}
	}
}

func normalizeLines(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	var kept []string
	blank := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(kept) > 0 && !blank {
				kept = append(kept, "")
				blank = true
			}
			continue
		}
		kept = append(kept, trimmed)
		blank = false
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func (m *mediaImport) picturePaint(block map[string]any) []byte {
	paint := m.source(block["paint"])
	if len(paint) == 0 {
		paint = m.source(block["img"])
	}
	if len(paint) == 0 {
		paint = m.source(block["pic"])
	}
	return paint
}

func pictureInkSize(block map[string]any, cfg image.Config) (int, int) {
	width, height := cfg.Width, cfg.Height
	if size, _ := block["size"].(string); width > 0 && size != "" {
		x, y, ok := strings.Cut(strings.Trim(size, "{}"), ",")
		if w, err := strconv.ParseFloat(strings.TrimSpace(x), 64); ok && err == nil && w > 0 && w <= 10000 {
			if h, err := strconv.ParseFloat(strings.TrimSpace(y), 64); err == nil && h > 0 && h <= 10000 {
				width, height = int(w+0.5), int(h+0.5)
			}
		}
	}
	return width, height
}

func (m *mediaImport) savePicture(block map[string]any) string {
	drawing := m.source(block["drawing"])
	if len(drawing) == 0 {
		for _, field := range []string{"paint", "img", "pic"} {
			val := block[field]
			if key, ok := val.(string); ok {
				if name, saved := m.savedKeys[key]; saved {
					return name
				}
				if raw := m.sources[key]; len(raw) > 0 {
					return m.saveImage(raw)
				}
				continue
			}
			if raw, ok := val.([]byte); ok && len(raw) > 0 {
				return m.saveImage(raw)
			}
		}
		return ""
	}
	return m.saveImage(m.picture(block))
}

func toRGBAInPlace(img image.Image) *image.RGBA {
	switch v := img.(type) {
	case *image.RGBA:
		return v
	case *image.NRGBA:
		pix := v.Pix
		for i := 0; i < len(pix); i += 4 {
			a := uint32(pix[i+3])
			if a == 255 {
				continue
			}
			if a == 0 {
				pix[i+0], pix[i+1], pix[i+2] = 0, 0, 0
				continue
			}
			pix[i+0] = uint8((uint32(pix[i+0])*a + 127) / 255)
			pix[i+1] = uint8((uint32(pix[i+1])*a + 127) / 255)
			pix[i+2] = uint8((uint32(pix[i+2])*a + 127) / 255)
		}
		return &image.RGBA{Pix: v.Pix, Stride: v.Stride, Rect: v.Rect}
	default:
		bounds := img.Bounds()
		dst := image.NewRGBA(bounds)
		draw.Draw(dst, bounds, img, bounds.Min, draw.Src)
		return dst
	}
}

func (m *mediaImport) inkCandidates(data []byte) [][]byte {
	if wrapped, ok := unarchive(data).(map[string]any); ok {
		return [][]byte{m.source(wrapped["drawing2"]), m.source(wrapped["drawing1"])}
	}
	return [][]byte{data}
}

func (m *mediaImport) picture(block map[string]any) []byte {
	paint := m.picturePaint(block)
	cfg, _, _ := imageConfig(paint)
	drawing := m.source(block["drawing"])
	if len(drawing) == 0 {
		return paint
	}
	width, height := pictureInkSize(block, cfg)

	// Fast pure-Go composite path: for unshared drawings over a base clip,
	// rasterize strokes directly onto the decoded base RGBA image (avoiding a
	// separate overlay RGBA buffer, draw.Draw pass, and intermediate PNG).
	if m.ink == "" && cfg.Width > 0 {
		candidates := m.inkCandidates(drawing)
		if len(candidates) == 1 && len(candidates[0]) > 0 {
			key := inkCacheKey{
				hash:   sha256.Sum256(candidates[0]),
				width:  width,
				height: height,
				pixels: cfg.Width,
			}
			shared := m.inkCounts == nil || m.inkCounts[key] > 1
			if !shared {
				strokes, ok := parsePKDrawing(candidates[0])
				if !ok || len(strokes) == 0 {
					return paint
				}
				release := m.acquireLargeCanvas(int64(cfg.Width) * int64(cfg.Height))
				defer release()
				base, _, err := image.Decode(bytes.NewReader(paint))
				if err != nil {
					return paint
				}
				dst := toRGBAInPlace(base)
				if !compositePKStrokesOnto(dst, strokes, float64(width), float64(height), cfg.Width) {
					return paint
				}
				if out := encodeFastPNG(dst); len(out) > 0 {
					return out
				}
				return paint
			}
		}
	}

	wantPNGOnly := cfg.Width == 0
	inkPNG, inkRGBA := m.renderInk(drawing, width, height, cfg.Width, wantPNGOnly)
	if len(inkPNG) == 0 && inkRGBA == nil {
		return paint
	}
	if wantPNGOnly {
		if len(inkPNG) > 0 {
			return inkPNG
		}
		return encodeFastPNG(inkRGBA)
	}
	// PencilKit exports annotate the same canvas as the original raster clip.
	release := m.acquireLargeCanvas(int64(cfg.Width) * int64(cfg.Height))
	defer release()
	base, _, err := image.Decode(bytes.NewReader(paint))
	if err != nil {
		return paint
	}
	var overlay image.Image
	if inkRGBA != nil {
		overlay = inkRGBA
	} else {
		overlay, _, err = image.Decode(bytes.NewReader(inkPNG))
		if err != nil {
			return paint
		}
	}
	composite := toRGBAInPlace(base)
	draw.Draw(composite, composite.Bounds(), overlay, overlay.Bounds().Min, draw.Over)
	if out := encodeFastPNG(composite); len(out) > 0 {
		return out
	}
	return paint
}

func (m *mediaImport) source(value any) []byte {
	switch v := value.(type) {
	case string:
		return m.sources[v]
	case []byte:
		return v
	}
	return nil
}

func (m *mediaImport) renderInk(data []byte, width, height, pixels int, wantPNGOnly bool) ([]byte, *image.RGBA) {
	for _, candidate := range m.inkCandidates(data) {
		if len(candidate) == 0 {
			continue
		}
		key := inkCacheKey{
			hash:   sha256.Sum256(candidate),
			width:  width,
			height: height,
			pixels: pixels,
		}
		shared := m.inkCounts == nil || m.inkCounts[key] > 1
		if !shared {
			pngBytes, rgba := m.renderInkCandidate(candidate, width, height, pixels, wantPNGOnly)
			if len(pngBytes) > 0 || rgba != nil {
				return pngBytes, rgba
			}
			continue
		}
		if pngBytes := m.renderInkMemoized(key, candidate, width, height, pixels); len(pngBytes) > 0 {
			return pngBytes, nil
		}
	}
	return nil, nil
}

func (m *mediaImport) renderInkMemoized(key inkCacheKey, candidate []byte, width, height, pixels int) []byte {
	total := 0
	if m.inkCounts != nil {
		total = m.inkCounts[key]
	}

	m.inkMu.Lock()
	if m.inkCache == nil {
		m.inkCache = make(map[inkCacheKey]*inkCacheEntry)
	}
	if entry, ok := m.inkCache[key]; ok {
		m.inkMu.Unlock()
		<-entry.ready
		res := entry.png
		if total > 1 && entry.rem.Add(-1) == 0 {
			m.inkMu.Lock()
			if m.inkCache[key] == entry {
				delete(m.inkCache, key)
			}
			m.inkMu.Unlock()
		}
		return res
	}
	entry := &inkCacheEntry{ready: make(chan struct{})}
	if total > 1 {
		entry.rem.Store(int32(total))
	}
	m.inkCache[key] = entry
	m.inkMu.Unlock()

	pngBytes, rgba := m.renderInkCandidate(candidate, width, height, pixels, true)
	if len(pngBytes) == 0 && rgba != nil {
		pngBytes = encodeFastPNG(rgba)
	}
	entry.png = pngBytes
	close(entry.ready)
	if total > 1 && entry.rem.Add(-1) == 0 {
		m.inkMu.Lock()
		if m.inkCache[key] == entry {
			delete(m.inkCache, key)
		}
		m.inkMu.Unlock()
	}
	// Return the cached PNG bytes to Consumer 1 as well as Consumers 2..N so
	// every consumer composites the exact same pixel representation and
	// produces identical deterministic SHA-256 hashes.
	return pngBytes
}

func (m *mediaImport) renderInkCandidate(candidate []byte, width, height, pixels int, wantPNGOnly bool) ([]byte, *image.RGBA) {
	strokes, parsed := parsePKDrawing(candidate)
	if parsed && len(strokes) == 0 {
		// Valid PencilKit drawing with zero visible strokes: skip external
		// renderer and Go rasterizer completely.
		return nil, nil
	}
	if m.ink != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		command := exec.CommandContext(ctx, m.ink, strconv.Itoa(width), strconv.Itoa(height), strconv.Itoa(pixels))
		command.Stdin = bytes.NewReader(candidate)
		out, err := command.Output()
		cancel()
		if err == nil && len(out) > 0 {
			return out, nil
		}
	}
	if !parsed || len(strokes) == 0 {
		return nil, nil
	}
	rectX, rectY, scale, outW, outH, ok := pkCanvasLayout(strokes, float64(width), float64(height), pixels)
	if !ok {
		return nil, nil
	}
	release := m.acquireLargeCanvas(int64(outW) * int64(outH))
	defer release()
	canvas := image.NewRGBA(image.Rect(0, 0, outW, outH))
	rasterizePKStrokesOnto(canvas, strokes, rectX, rectY, scale, outW, outH)
	if wantPNGOnly {
		return encodeFastPNG(canvas), nil
	}
	return nil, canvas
}

func imageConfig(data []byte) (image.Config, string, bool) {
	cfg, ext, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 28_000_000 {
		return image.Config{}, "", false
	}
	return cfg, ext, true
}

func (m *mediaImport) saveImage(data []byte) string {
	_, ext, ok := imageConfig(data)
	if !ok {
		return ""
	}
	name := fmt.Sprintf("%x.%s", sha256.Sum256(data), ext)
	if m.sink != nil {
		if err := m.sink.save(name, data); err != nil {
			m.recordErr(err)
			return ""
		}
		return name
	}
	m.filesMu.Lock()
	if m.files == nil {
		m.files = make(map[string][]byte)
	}
	m.files[name] = data
	m.filesMu.Unlock()
	return name
}
