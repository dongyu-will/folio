package notebook

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const maxImportSize = 512 << 20

var backupSuffix = regexp.MustCompile(`\s*\(\d{4}-\d\d-\d\d-\d\d-\d\d-\d\d\)$`)

type Book struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Source       string     `json:"source"`
	Roots        []string   `json:"roots"`
	Notes        []Note     `json:"notes"`
	MediaVersion int        `json:"mediaVersion,omitempty"`
	SourceTitle  string     `json:"sourceTitle,omitempty"`
	DeletedNotes []string   `json:"deletedNotes,omitempty"`
	Documents    []Document `json:"documents,omitempty"`
	PDFsScanned  bool       `json:"pdfsScanned,omitempty"`
}

type Note struct {
	ID             string          `json:"id"`
	Title          string          `json:"title"`
	Body           string          `json:"body"`
	Excerpt        string          `json:"excerpt,omitempty"`
	Page           *int64          `json:"page,omitempty"`
	Children       []string        `json:"children,omitempty"`
	Review         string          `json:"review,omitempty"` // Preserved for libraries written by earlier versions.
	Clip           string          `json:"clip,omitempty"`
	Blocks         []ContentBlock  `json:"blocks,omitempty"`
	Color          *int            `json:"color,omitempty"`
	Style          int             `json:"style,omitempty"`
	Collapsed      bool            `json:"collapsed,omitempty"`
	LayoutMode     *int            `json:"layoutMode,omitempty"`
	HighlightRects []HighlightRect `json:"highlightRects,omitempty"`
	MaskRects      []HighlightRect `json:"maskRects,omitempty"`
	BookMD5        string          `json:"bookMD5,omitempty"`
	BookTitle      string          `json:"bookTitle,omitempty"`
	CreatedAt      int64           `json:"createdAt,omitempty"`
	UpdatedAt      int64           `json:"updatedAt,omitempty"`
	Tags           []string        `json:"tags,omitempty"`
	Links          []string        `json:"links,omitempty"`
	TitleEdited    bool            `json:"titleEdited,omitempty"`
	BodyEdited     bool            `json:"bodyEdited,omitempty"`
}

type sourceNote struct {
	Note
	topic             string
	parent            string
	ztype             int
	zindex            int
	hasZIndex         bool
	dummyOnly         bool
	sketchOrDummyOnly bool
}

func readMarginNote(path, ink string) ([]Book, map[string][]byte, *bookConfigIndex, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxImportSize {
		return nil, nil, nil, errors.New("请选择小于 512 MB 的 MarginNote 笔记文件")
	}

	read, err := openPayload(path)
	if err != nil {
		return nil, nil, nil, err
	}
	defer read.Close()

	temp, err := os.CreateTemp("", "mgnote-import-*.sqlite")
	if err != nil {
		return nil, nil, nil, err
	}
	defer os.Remove(temp.Name())
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(read, maxImportSize+1))
	closeErr := temp.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return nil, nil, nil, err
	}
	if n > maxImportSize {
		return nil, nil, nil, errors.New("解压后的笔记数据库超过 512 MB")
	}

	uri := (&url.URL{Scheme: "file", Path: filepath.ToSlash(temp.Name())}).String()
	db, err := sql.Open("sqlite", uri+"?mode=ro&immutable=1&_pragma=trusted_schema(OFF)")
	if err != nil {
		return nil, nil, nil, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	media, err := readMedia(ctx, db, ink)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("无法读取笔记附件：%w", err)
	}
	configs := readBookConfigs(ctx, db)
	books, err := readBooks(ctx, db, hex.EncodeToString(hash.Sum(nil)), filepath.Base(path), media, configs)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("无法读取 MarginNote 笔记数据库：%w", err)
	}
	if len(books) == 0 {
		return nil, nil, nil, errors.New("文件里没有可导入的笔记")
	}
	return books, pruneUnusedMedia(books, media.files), configs, nil
}

func pruneUnusedMedia(books []Book, files map[string][]byte) map[string][]byte {
	if len(files) == 0 {
		return files
	}
	used := make(map[string]bool)
	for _, b := range books {
		for _, n := range b.Notes {
			if n.Clip != "" {
				used[n.Clip] = true
			}
			for _, blk := range n.Blocks {
				if blk.Image != "" {
					used[blk.Image] = true
				}
			}
		}
	}
	out := make(map[string][]byte, len(used))
	for name, data := range files {
		if used[name] {
			out[name] = data
		}
	}
	return out
}

type zipPayload struct {
	io.ReadCloser
	archive *zip.ReadCloser
}

func (p zipPayload) Close() error { return errors.Join(p.ReadCloser.Close(), p.archive.Close()) }

func openPayload(path string) (io.ReadCloser, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".marginnotes":
		return os.Open(path)
	case ".marginpkg":
		archive, err := zip.OpenReader(path)
		if err != nil {
			return nil, fmt.Errorf("无法打开 MarginNote 压缩包：%w", err)
		}
		var payload *zip.File
		for _, entry := range archive.File {
			if !entry.Mode().IsRegular() || strings.ToLower(filepath.Ext(entry.Name)) != ".marginnotes" ||
				strings.HasPrefix(entry.Name, "__MACOSX/") || strings.HasPrefix(filepath.Base(entry.Name), "._") {
				continue
			}
			if payload != nil {
				archive.Close()
				return nil, errors.New("压缩包里有多个笔记数据库，请解压后逐个导入 .marginnotes")
			}
			payload = entry
		}
		if payload == nil || payload.UncompressedSize64 > maxImportSize {
			archive.Close()
			return nil, errors.New("压缩包缺少可用的 .marginnotes 数据库")
		}
		r, err := payload.Open()
		if err != nil {
			archive.Close()
			return nil, err
		}
		return zipPayload{r, archive}, nil
	default:
		return nil, errors.New("请选择 .marginpkg、.marginnotes 或 .pdf 文件")
	}
}

const coreDataEpochOffset int64 = 978307200

type bookConfig struct {
	md5      string
	md5Long  string
	title    string
	currPage int
	toc      []TOCEntry
}

type bookConfigIndex struct {
	all     []bookConfig
	byKey   map[string]bookConfig
	byTitle map[string]bookConfig
}

func (idx *bookConfigIndex) lookup(key string) (bookConfig, bool) {
	if idx == nil {
		return bookConfig{}, false
	}
	k := strings.ToLower(strings.TrimSpace(key))
	if k == "" {
		return bookConfig{}, false
	}
	if cfg, ok := idx.byKey[k]; ok {
		return cfg, true
	}
	if len(k) > 32 {
		if cfg, ok := idx.byKey[k[:32]]; ok {
			return cfg, true
		}
	}
	return bookConfig{}, false
}

func (idx *bookConfigIndex) matchDocument(stem, docMD5, docSHA256 string, singleDoc bool) (bookConfig, bool) {
	if idx == nil {
		return bookConfig{}, false
	}
	for _, key := range []string{stem, docMD5, docSHA256} {
		if cfg, ok := idx.lookup(key); ok {
			return cfg, true
		}
	}
	if s := strings.ToLower(strings.TrimSpace(stem)); s != "" {
		if cfg, ok := idx.byTitle[s]; ok {
			return cfg, true
		}
	}
	if singleDoc && len(idx.all) == 1 {
		return idx.all[0], true
	}
	return bookConfig{}, false
}

func tableColumns(ctx context.Context, db *sql.DB, table string) map[string]bool {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil
	}
	defer rows.Close()
	cols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return nil
		}
		cols[strings.ToUpper(name)] = true
	}
	return cols
}

func readBookConfigs(ctx context.Context, db *sql.DB) *bookConfigIndex {
	var present bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name='ZBOOKCONFIG')").Scan(&present); err != nil || !present {
		return nil
	}
	cols := tableColumns(ctx, db, "ZBOOKCONFIG")
	if len(cols) == 0 {
		return nil
	}
	colOr := func(name, fallback string) string {
		if cols[name] {
			return "COALESCE(" + name + ", " + fallback + ")"
		}
		return fallback
	}
	query := "SELECT " +
		colOr("ZMD5", "''") + ", " +
		colOr("ZMD5LONG", "''") + ", " +
		colOr("ZTITLE", "''") + ", " +
		colOr("ZCURRPAGE", "0") + ", " +
		colOr("ZOPTIONS", "''") +
		" FROM ZBOOKCONFIG ORDER BY Z_PK"
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil
	}
	defer rows.Close()

	idx := &bookConfigIndex{
		byKey:   make(map[string]bookConfig),
		byTitle: make(map[string]bookConfig),
	}
	for rows.Next() {
		var cfg bookConfig
		var options string
		if err := rows.Scan(&cfg.md5, &cfg.md5Long, &cfg.title, &cfg.currPage, &options); err != nil {
			return idx
		}
		cfg.md5 = strings.TrimSpace(cfg.md5)
		cfg.md5Long = strings.TrimSpace(cfg.md5Long)
		cfg.title = strings.TrimSpace(cfg.title)
		if cfg.currPage < 0 {
			cfg.currPage = 0
		}
		cfg.toc = parseBookOptionsTOC(options)
		idx.all = append(idx.all, cfg)
		for _, key := range []string{cfg.md5, cfg.md5Long} {
			if k := strings.ToLower(key); k != "" {
				idx.byKey[k] = cfg
				if len(k) > 32 {
					if _, exists := idx.byKey[k[:32]]; !exists {
						idx.byKey[k[:32]] = cfg
					}
				}
			}
		}
		if cfg.title != "" {
			idx.byTitle[strings.ToLower(cfg.title)] = cfg
		}
	}
	return idx
}

func parseBookOptionsTOC(raw string) []TOCEntry {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.Contains(raw, "AIBookTocMap") {
		return nil
	}
	var opt map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &opt); err != nil {
		return nil
	}
	tocRaw, ok := opt["AIBookTocMap"]
	if !ok || len(tocRaw) == 0 {
		return nil
	}
	// MarginNote stores AIBookTocMap as a JSON-encoded string inside ZOPTIONS JSON.
	var encoded string
	if err := json.Unmarshal(tocRaw, &encoded); err == nil {
		tocRaw = []byte(strings.TrimSpace(encoded))
	}
	if len(tocRaw) == 0 {
		return nil
	}
	var items []struct {
		Title  string `json:"title"`
		Label  string `json:"label"`
		PageNo int    `json:"pageNo"`
		Page   int    `json:"page"`
		Link   int    `json:"link"`
		Level  int    `json:"level"`
	}
	if err := json.Unmarshal(tocRaw, &items); err != nil {
		return nil
	}
	var out []TOCEntry
	for _, it := range items {
		title := strings.TrimSpace(it.Title)
		if title == "" {
			title = strings.TrimSpace(it.Label)
		}
		page := it.PageNo
		if page <= 0 {
			page = it.Page
		}
		if page <= 0 {
			page = it.Link
		}
		if title == "" || page <= 0 {
			continue
		}
		level := it.Level
		if level < 0 {
			level = 0
		}
		out = append(out, TOCEntry{Title: title, Page: page, Level: level})
	}
	return out
}

func readBooks(ctx context.Context, db *sql.DB, digest, source string, media *mediaImport, configs *bookConfigIndex) ([]Book, error) {
	topicCols := tableColumns(ctx, db, "ZTOPIC")
	hashtagsExpr := "''"
	if topicCols["ZHASHTAGS"] {
		hashtagsExpr = "COALESCE(ZHASHTAGS, '')"
	}
	rows, err := db.QueryContext(ctx, `
		SELECT COALESCE(ZTOPICID, ''), COALESCE(ZTITLE, ''), COALESCE(ZMINDLINKS, ''), `+hashtagsExpr+`
		FROM ZTOPIC ORDER BY Z_PK`)
	if err != nil {
		return nil, err
	}
	type topic struct{ id, title, roots, hashtags string }
	var topics []topic
	for rows.Next() {
		var t topic
		if err := rows.Scan(&t.id, &t.title, &t.roots, &t.hashtags); err != nil {
			rows.Close()
			return nil, err
		}
		topics = append(topics, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	notes, err := readNotes(ctx, db, media, configs)
	if err != nil {
		return nil, err
	}
	books := []Book{}
	for _, t := range topics {
		list := filterSourceNotes(notes[t.id])
		delete(notes, t.id)
		if len(list) == 0 {
			continue
		}
		topicRoots := splitLinks(t.roots)
		applyTopicHashtags(list, topicRoots, t.hashtags)
		title := strings.TrimSpace(backupSuffix.ReplaceAllString(t.title, ""))
		if title == "" {
			title = strings.TrimSuffix(source, filepath.Ext(source))
		}
		book := Book{ID: digest + "/" + t.id, Title: title, Source: source, MediaVersion: mediaVersion(media.ink)}
		book.Notes, book.Roots = connectNotes(list, topicRoots)
		books = append(books, book)
	}
	// Older exports may carry notes without a matching topic row.
	if len(notes) > 0 {
		var orphans []sourceNote
		for _, list := range notes {
			orphans = append(orphans, list...)
		}
		orphans = filterSourceNotes(orphans)
		if len(orphans) > 0 {
			book := Book{ID: digest, Title: strings.TrimSuffix(source, filepath.Ext(source)), Source: source, MediaVersion: mediaVersion(media.ink)}
			book.Notes, book.Roots = connectNotes(orphans, nil)
			books = append(books, book)
		}
	}
	return books, nil
}

func applyTopicHashtags(list []sourceNote, topicRoots []string, raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(list) == 0 {
		return
	}
	byID := make(map[string]int, len(list))
	for i, n := range list {
		byID[n.ID] = i
	}
	var mapped map[string][]string
	if err := json.Unmarshal([]byte(raw), &mapped); err == nil && len(mapped) > 0 {
		for k, vals := range mapped {
			if idx, ok := byID[k]; ok {
				appendTags(&list[idx].Note, vals...)
				continue
			}
			tag := strings.TrimSpace(strings.TrimPrefix(k, "#"))
			if tag == "" {
				continue
			}
			for _, noteID := range vals {
				if idx, ok := byID[noteID]; ok {
					appendTags(&list[idx].Note, tag)
				}
			}
		}
		return
	}
	var topicTags []string
	var arr []string
	if err := json.Unmarshal([]byte(raw), &arr); err == nil && len(arr) > 0 {
		topicTags = arr
	} else {
		for _, tok := range strings.FieldsFunc(raw, func(r rune) bool {
			return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == ',' || r == '|' || r == ';'
		}) {
			if t := strings.TrimSpace(strings.TrimPrefix(tok, "#")); t != "" {
				topicTags = append(topicTags, t)
			}
		}
	}
	if len(topicTags) == 0 {
		return
	}
	applied := false
	for _, rootID := range topicRoots {
		if idx, ok := byID[rootID]; ok {
			appendTags(&list[idx].Note, topicTags...)
			applied = true
		}
	}
	if !applied {
		appendTags(&list[0].Note, topicTags...)
	}
}

func filterSourceNotes(list []sourceNote) []sourceNote {
	if len(list) == 0 {
		return nil
	}
	isGroupParent := make(map[string]bool)
	for _, n := range list {
		if n.parent != "" && n.parent != n.ID {
			isGroupParent[n.parent] = true
		}
	}
	out := make([]sourceNote, 0, len(list))
	for _, n := range list {
		hasChildren := len(n.Children) > 0 || isGroupParent[n.ID]
		hasText := strings.TrimSpace(n.Title) != "" || strings.TrimSpace(n.Body) != "" || strings.TrimSpace(n.Excerpt) != ""
		// Drop completely empty ghost notes (e.g. MarginNote tombstones / DummyNotes).
		if !hasText && n.Clip == "" && len(n.Blocks) == 0 && len(n.Links) == 0 && len(n.HighlightRects) == 0 && !hasChildren {
			continue
		}
		// Drop canvas-level untitled ZTYPE==4 SketchNote/DummyNote overlays (ZZINDEX < 0).
		if n.ztype == 4 && (!n.hasZIndex || n.zindex < 0) && n.sketchOrDummyOnly && !hasText && n.Clip == "" && !hasChildren {
			continue
		}
		out = append(out, n)
	}
	return out
}

func readNotes(ctx context.Context, db *sql.DB, media *mediaImport, configs *bookConfigIndex) (map[string][]sourceNote, error) {
	cols := tableColumns(ctx, db, "ZBOOKNOTE")
	hasMedia := cols["ZHIGHLIGHT_PIC"] && cols["ZNOTES"]
	mediaExpr := "NULL, NULL"
	if hasMedia {
		mediaExpr = "ZHIGHLIGHT_PIC, ZNOTES"
	}
	colOr := func(name, fallback string) string {
		if cols[name] {
			return "COALESCE(" + name + ", " + fallback + ")"
		}
		return fallback
	}
	dateOrNull := func(name string) string {
		if cols[name] {
			return name
		}
		return "NULL"
	}
	hasZIndex := cols["ZZINDEX"]
	query := `
		SELECT ZNOTEID, COALESCE(ZTOPICID, ''), COALESCE(ZGROUPNOTEID, ''),
		       COALESCE(ZNOTETITLE, ''), COALESCE(ZNOTES_TEXT, ''),
		       COALESCE(ZHIGHLIGHT_TEXT, ''), ZSTARTPAGE, COALESCE(ZMINDLINKS, ''), ` +
		mediaExpr + `, ` +
		colOr("ZHIGHLIGHT_STYLE", "''") + `, ` +
		colOr("ZMINDCLOSE", "0") + `, ` +
		colOr("ZRECOGNIZE_TEXT", "''") + `, ` +
		colOr("ZBOOKMD5", "''") + `, ` +
		dateOrNull("ZHIGHLIGHT_DATE") + `, ` +
		dateOrNull("ZNOTE_DATE") + `, ` +
		colOr("ZTYPE", "0") + `, ` +
		colOr("ZZINDEX", "0") + `
		FROM ZBOOKNOTE WHERE ZNOTEID IS NOT NULL AND ZNOTEID <> ''
		ORDER BY Z_PK`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grouped := map[string][]sourceNote{}
	count := 0
	for rows.Next() {
		count++
		if count > 20000 {
			return nil, errors.New("单次最多导入 20,000 条笔记")
		}
		var n sourceNote
		var links, highlightStyle, recognizeText, bookMD5 string
		var collapsed int
		var clip, blocks []byte
		var highlightDate, noteDate sql.NullFloat64
		if err := rows.Scan(
			&n.ID, &n.topic, &n.parent, &n.Title, &n.Body, &n.Excerpt, &n.Page, &links,
			&clip, &blocks, &highlightStyle, &collapsed, &recognizeText, &bookMD5,
			&highlightDate, &noteDate, &n.ztype, &n.zindex,
		); err != nil {
			return nil, err
		}
		n.hasZIndex = hasZIndex
		if htmlTagHint.MatchString(n.Body) {
			n.Body = stripHTML(n.Body)
		}
		n.Children = splitLinks(links)
		n.Color, n.Style = parseHighlightStyle(highlightStyle)
		n.Collapsed = collapsed != 0
		n.LayoutMode = parseLayoutMode(recognizeText)
		n.BookMD5 = strings.TrimSpace(bookMD5)
		if cfg, ok := configs.lookup(n.BookMD5); ok && cfg.title != "" {
			n.BookTitle = cfg.title
		}
		n.CreatedAt = coreDataToUnix(highlightDate)
		n.UpdatedAt = coreDataToUnix(noteDate)
		info := media.attach(&n.Note, clip, blocks)
		n.dummyOnly = info.dummyOnly
		n.sketchOrDummyOnly = info.sketchOrDummyOnly
		grouped[n.topic] = append(grouped[n.topic], n)
	}
	return grouped, rows.Err()
}

func parseLayoutMode(raw string) *int {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.Contains(raw, "layoutMode") {
		return nil
	}
	var payload struct {
		MapConfig *struct {
			LayoutMode *int `json:"layoutMode"`
		} `json:"mapConfig"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil || payload.MapConfig == nil || payload.MapConfig.LayoutMode == nil {
		return nil
	}
	mode := *payload.MapConfig.LayoutMode
	return &mode
}

func coreDataToUnix(ts sql.NullFloat64) int64 {
	if !ts.Valid || math.IsNaN(ts.Float64) || math.IsInf(ts.Float64, 0) || ts.Float64 <= 0 {
		return 0
	}
	return int64(math.Round(ts.Float64)) + coreDataEpochOffset
}

func parseHighlightStyle(raw string) (*int, int) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, 0
	}
	hexPart := strings.TrimPrefix(strings.ToLower(raw), "mbooks-annotation")
	if hexPart == raw || hexPart == "" {
		return nil, 0
	}
	val, err := strconv.ParseInt(hexPart, 16, 32)
	if err != nil || val < 0 {
		return nil, 0
	}
	color := int(val & 0x0f)
	style := int((val >> 4) & 0x0f)
	return &color, style
}

func splitLinks(links string) []string {
	var ids []string
	for _, link := range strings.Split(links, "|") {
		if id := strings.TrimSpace(link); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// The topic and each node store their own ordered lists of linked IDs.
// Keep shared and cyclic references in the model. The UI can render each
// reference once per branch while retaining links to the same editable note.
func connectNotes(source []sourceNote, topicRoots []string) ([]Note, []string) {
	notes := make([]Note, 0, len(source))
	index := make(map[string]int, len(source))
	for _, n := range source {
		if _, exists := index[n.ID]; !exists {
			index[n.ID] = len(notes)
			notes = append(notes, n.Note)
		}
	}
	linked := make(map[string]bool, len(notes))
	prune := func(owner string, ids []string) []string {
		var result []string
		seen := make(map[string]bool)
		for _, id := range ids {
			if _, exists := index[id]; exists && id != owner && !seen[id] {
				seen[id], linked[id] = true, true
				result = append(result, id)
			}
		}
		return result
	}
	roots := prune("", topicRoots)
	for i := range notes {
		notes[i].Children = prune(notes[i].ID, notes[i].Children)
	}
	for _, n := range source {
		if parent, ok := index[n.parent]; ok && !linked[n.ID] && n.ID != n.parent {
			notes[parent].Children = append(notes[parent].Children, n.ID)
			linked[n.ID] = true
		}
	}
	for _, n := range notes {
		if !linked[n.ID] {
			roots = append(roots, n.ID)
		}
	}
	// Include disconnected components made entirely of linked nodes, such
	// as a cycle, so each imported note remains reachable.
	seen := make(map[string]bool, len(notes))
	visit := func(root string) {
		stack := []string{root}
		for len(stack) > 0 {
			id := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if !seen[id] {
				seen[id] = true
				stack = append(stack, notes[index[id]].Children...)
			}
		}
	}
	for _, id := range roots {
		visit(id)
	}
	for _, n := range notes {
		if !seen[n.ID] {
			roots = append(roots, n.ID)
			visit(n.ID)
		}
	}
	return notes, roots
}
