package notebook

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
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

const maxPDFSize = 512 << 20

var pdfID = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (s *Store) DocumentPath(id string) string {
	if !pdfID.MatchString(id) {
		return ""
	}
	return filepath.Join(s.dir, "documents", id+".pdf")
}

func (s *Store) saveDocument(source io.Reader, title string) (Document, string, error) {
	input := bufio.NewReader(source)
	header, _ := input.Peek(1024)
	if !bytes.Contains(header, []byte("%PDF-")) {
		return Document{}, "", errors.New("文件不包含可读取的 PDF")
	}
	dir := filepath.Join(s.dir, "documents")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Document{}, "", err
	}
	temp, err := os.CreateTemp(dir, ".pdf-*")
	if err != nil {
		return Document{}, "", err
	}
	defer os.Remove(temp.Name())
	hash := sha256.New()
	md5Hash := md5.New()
	size, copyErr := io.Copy(io.MultiWriter(temp, hash, md5Hash), io.LimitReader(input, maxPDFSize+1))
	if err := errors.Join(copyErr, temp.Sync(), temp.Close()); err != nil {
		return Document{}, "", err
	}
	if size > maxPDFSize {
		return Document{}, "", errors.New("PDF 超过 512 MB")
	}
	doc := Document{ID: fmt.Sprintf("%x", hash.Sum(nil)), Title: title}
	if err := os.Rename(temp.Name(), s.DocumentPath(doc.ID)); err != nil {
		return Document{}, "", err
	}
	return doc, fmt.Sprintf("%x", md5Hash.Sum(nil)), nil
}

func (s *Store) importPDF(path string) (ImportResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return ImportResult{}, err
	}
	defer f.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	library, err := s.read()
	if err != nil {
		return ImportResult{}, err
	}
	source := filepath.Base(path)
	title := strings.TrimSpace(strings.TrimSuffix(source, filepath.Ext(source)))
	if title == "" {
		title = "未命名 PDF"
	}
	doc, _, err := s.saveDocument(f, title)
	if err != nil {
		return ImportResult{}, err
	}
	book := Book{ID: "pdf-" + doc.ID, Title: doc.Title, Source: source, Documents: []Document{doc}}
	if indexBook(&library, book.ID) >= 0 {
		return ImportResult{Library: library, BookID: book.ID}, nil
	}
	library.Books = append(library.Books, book)
	if err := s.writeAllShards(library, map[string]bool{book.ID: true}); err != nil {
		return ImportResult{}, err
	}
	return ImportResult{Library: library, BookID: book.ID, Added: 1}, nil
}

func (s *Store) importPackagePDFs(path string, configs *bookConfigIndex) ([]Document, error) {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer archive.Close()
	var pdfEntries []*zip.File
	for _, entry := range archive.File {
		name := filepath.Base(entry.Name)
		if !entry.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(name), ".pdf") ||
			strings.HasPrefix(entry.Name, "__MACOSX/") || strings.HasPrefix(name, "._") {
			continue
		}
		pdfEntries = append(pdfEntries, entry)
	}
	var documents []Document
	var total uint64
	seen := make(map[string]bool)
	for _, entry := range pdfEntries {
		name := filepath.Base(entry.Name)
		if entry.UncompressedSize64 > maxPDFSize || total+entry.UncompressedSize64 > 2*maxPDFSize {
			return nil, errors.New("笔记包内的 PDF 超过导入大小限制")
		}
		total += entry.UncompressedSize64
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
		doc, docMD5, err := s.saveDocument(file, title)
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
