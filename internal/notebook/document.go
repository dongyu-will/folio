package notebook

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

type TOCEntry struct {
	Title string `json:"title"`
	Page  int    `json:"page"`
	Level int    `json:"level,omitempty"`
}

type Document struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	TOC      []TOCEntry `json:"toc,omitempty"`
	LastPage int        `json:"lastPage,omitempty"`
}

const copyBufferSize = 1 << 20

var pdfID = regexp.MustCompile(`^[a-f0-9]{64}$`)

type progressReader struct {
	r          io.Reader
	done       int64
	total      int64
	lastBytes  int64
	lastReport time.Time
	onProgress func(done, total int64)
}

func newProgressReader(r io.Reader, total int64, onProgress func(done, total int64)) io.Reader {
	if onProgress == nil {
		return r
	}
	return &progressReader{r: r, total: total, onProgress: onProgress}
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.done += int64(n)
		now := time.Now()
		if p.lastReport.IsZero() || p.done-p.lastBytes >= 4<<20 || now.Sub(p.lastReport) >= 150*time.Millisecond || (p.total > 0 && p.done >= p.total) {
			p.lastBytes = p.done
			p.lastReport = now
			p.onProgress(p.done, p.total)
		}
	}
	return n, err
}

func (s *Store) DocumentPath(id string) string {
	if !pdfID.MatchString(id) {
		return ""
	}
	return filepath.Join(s.dir, "documents", id+".pdf")
}

func openPDFReader(ctx context.Context, r io.Reader) (*bufio.Reader, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	input := bufio.NewReaderSize(withContextReader(ctx, r), 64<<10)
	header, _ := input.Peek(1024)
	if !bytes.Contains(header, []byte("%PDF-")) {
		return nil, errors.New("文件不包含可读取的 PDF")
	}
	return input, nil
}

func copyAndHashPDF(dst io.Writer, input io.Reader, expectedSize int64, onProgress func(done, total int64)) (docID, docMD5 string, err error) {
	hash := sha256.New()
	md5Hash := md5.New()
	mw := io.MultiWriter(hash, md5Hash)
	if dst != nil {
		mw = io.MultiWriter(dst, hash, md5Hash)
	}
	buf := make([]byte, copyBufferSize)
	if _, err := io.CopyBuffer(mw, newProgressReader(input, expectedSize, onProgress), buf); err != nil {
		return "", "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), fmt.Sprintf("%x", md5Hash.Sum(nil)), nil
}

func (s *Store) saveDocument(ctx context.Context, source io.Reader, title string, expectedSize int64, onProgress func(done, total int64)) (Document, string, error) {
	input, err := openPDFReader(ctx, source)
	if err != nil {
		return Document{}, "", err
	}
	dir := filepath.Join(s.dir, "documents")
	if err := checkAvailableSpace(dir, expectedSize); err != nil {
		return Document{}, "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Document{}, "", err
	}
	temp, err := os.CreateTemp(dir, ".pdf-*")
	if err != nil {
		return Document{}, "", mapDiskSpaceError(err)
	}
	defer os.Remove(temp.Name())
	docID, docMD5, copyErr := copyAndHashPDF(temp, input, expectedSize, onProgress)
	if err := mapDiskSpaceError(errors.Join(copyErr, temp.Sync(), temp.Close())); err != nil {
		return Document{}, "", err
	}
	doc := Document{ID: docID, Title: title}
	dst := s.DocumentPath(doc.ID)
	if _, err := os.Stat(dst); err == nil {
		return doc, docMD5, nil
	}
	if err := os.Rename(temp.Name(), dst); err != nil {
		return Document{}, "", mapDiskSpaceError(err)
	}
	return doc, docMD5, nil
}

func (s *Store) saveDocumentFromPath(ctx context.Context, path, title string, canMove bool, onProgress func(done, total int64)) (Document, string, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return Document{}, "", err
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return Document{}, "", err
	}
	var expectedSize int64 = -1
	if info, statErr := f.Stat(); statErr == nil && info.Size() >= 0 {
		expectedSize = info.Size()
	}
	if !canMove {
		defer f.Close()
		return s.saveDocument(ctx, f, title, expectedSize, onProgress)
	}

	input, err := openPDFReader(ctx, f)
	if err != nil {
		_ = f.Close()
		return Document{}, "", err
	}
	docID, docMD5, copyErr := copyAndHashPDF(nil, input, expectedSize, onProgress)
	if err := errors.Join(copyErr, f.Close()); err != nil {
		return Document{}, "", err
	}
	doc := Document{ID: docID, Title: title}
	dst := s.DocumentPath(doc.ID)
	if _, err := os.Stat(dst); err == nil {
		return doc, docMD5, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return Document{}, "", err
	}
	if err := os.Rename(path, dst); err == nil {
		return doc, docMD5, nil
	}

	// Cross-device rename failed; fall back to streaming copy.
	f2, err := os.Open(path)
	if err != nil {
		return Document{}, "", err
	}
	defer f2.Close()
	return s.saveDocument(ctx, f2, title, expectedSize, onProgress)
}

func (s *Store) importPDF(path string, opts ImportOptions) (ImportResult, error) {
	source := filepath.Base(path)
	title := strings.TrimSpace(strings.TrimSuffix(source, filepath.Ext(source)))
	if title == "" {
		title = "未命名 PDF"
	}
	progressFn := stageProgressFn(opts.OnProgress, ImportStageImportPDF, 1, 1)
	doc, _, err := s.saveDocumentFromPath(opts.Ctx, path, title, opts.CanMoveSource, progressFn)
	if err != nil {
		return ImportResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	library, err := s.read()
	if err != nil {
		return ImportResult{}, err
	}
	book := Book{ID: "pdf-" + doc.ID, Title: doc.Title, Source: source, Documents: []Document{doc}}
	if indexBook(&library, book.ID) >= 0 {
		return ImportResult{Library: library, BookID: book.ID}, nil
	}
	library.Books = append(library.Books, book)
	if err := s.writeAllShards(library, map[string]bool{book.ID: true}); err != nil {
		s.gcDocuments(library.Books[:len(library.Books)-1])
		return ImportResult{}, mapDiskSpaceError(err)
	}
	return ImportResult{Library: library, BookID: book.ID, Added: 1}, nil
}

func (s *Store) importPackagePDFs(ctx context.Context, path string, configs *bookConfigIndex, onProgress func(ImportProgress)) ([]Document, error) {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer archive.Close()
	var pdfEntries []*zip.File
	var totalExpected uint64
	for _, entry := range archive.File {
		name := filepath.Base(entry.Name)
		if !entry.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(name), ".pdf") ||
			strings.HasPrefix(entry.Name, "__MACOSX/") || strings.HasPrefix(name, "._") {
			continue
		}
		pdfEntries = append(pdfEntries, entry)
		if entry.UncompressedSize64 > math.MaxUint64-totalExpected {
			totalExpected = math.MaxUint64
		} else {
			totalExpected += entry.UncompressedSize64
		}
	}
	if len(pdfEntries) == 0 {
		return nil, nil
	}
	neededBytes := clampInt64Size(totalExpected)
	if err := checkAvailableSpace(filepath.Join(s.dir, "documents"), neededBytes); err != nil {
		return nil, err
	}

	var documents []Document
	seen := make(map[string]bool)
	for i, entry := range pdfEntries {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		name := filepath.Base(entry.Name)
		entrySize := entryUncompressedSize(entry)
		file, err := entry.Open()
		if err != nil {
			return nil, err
		}
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		title := stem
		hashNamed := pdfID.MatchString(title)
		if hashNamed {
			title = fmt.Sprintf("文档 %d", len(documents)+1)
		}
		progressFn := stageProgressFn(onProgress, ImportStageImportPDF, i+1, len(pdfEntries))
		doc, docMD5, err := s.saveDocument(ctx, file, title, entrySize, progressFn)
		if err := errors.Join(err, file.Close()); err != nil {
			return nil, fmt.Errorf("无法导入 PDF %s：%w", name, err)
		}
		if cfg, ok := configs.matchDocument(stem, docMD5, doc.ID, len(pdfEntries) == 1); ok {
			if cfg.title != "" && (hashNamed || doc.Title == "") {
				doc.Title = cfg.title
			}
			if cfg.currPage > 0 {
				doc.LastPage = cfg.currPage
			}
			if len(cfg.toc) > 0 {
				doc.TOC = slices.Clone(cfg.toc)
			}
		}
		if !seen[doc.ID] {
			seen[doc.ID] = true
			documents = append(documents, doc)
		}
	}
	return documents, nil
}

func (s *Store) gcDocuments(books []Book) {
	referenced := make(map[string]bool)
	for _, b := range books {
		for _, doc := range b.Documents {
			referenced[doc.ID] = true
		}
	}
	dir := filepath.Join(s.dir, "documents")
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".pdf")
		if ok && !entry.IsDir() && pdfID.MatchString(id) && !referenced[id] {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
