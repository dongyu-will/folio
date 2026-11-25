package notebook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type WebDAVConfig struct {
	BaseURL  string
	Username string
	Password string
	Client   *http.Client
}

type WebDAVSyncResult struct {
	Library         Library
	UploadedBooks   int
	DownloadedBooks int
	UploadedFiles   int
	DownloadedFiles int
	SyncedAt        int64
}

type webdavManifest struct {
	Version   int                       `json:"version"`
	UpdatedAt int64                     `json:"updatedAt"`
	Books     map[string]webdavBookMeta `json:"books"`
	Drawings  map[string]string         `json:"drawings,omitempty"`
}

type webdavBookMeta struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt int64  `json:"updatedAt"`
}

func (c WebDAVConfig) normalize() (string, *http.Client, error) {
	raw := strings.TrimSpace(c.BaseURL)
	if raw == "" {
		return "", nil, errors.New("请输入 WebDAV 服务器地址")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", nil, errors.New("WebDAV 地址格式无效，需以 http:// 或 https:// 开头")
	}
	base := strings.TrimRight(raw, "/")
	if !strings.HasSuffix(base, "/FolioSync") {
		base += "/FolioSync"
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	return base, client, nil
}

func (c WebDAVConfig) do(ctx context.Context, client *http.Client, method, targetURL string, body []byte, contentType string) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL, bodyReader)
	if err != nil {
		return nil, err
	}
	if c.Username != "" || c.Password != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return client.Do(req)
}

func (c WebDAVConfig) ensureDir(ctx context.Context, client *http.Client, dirURL string) error {
	resp, err := c.do(ctx, client, "MKCOL", strings.TrimRight(dirURL, "/")+"/", nil, "")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent, http.StatusMovedPermanently, http.StatusMethodNotAllowed:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return errors.New("WebDAV 认证失败，请检查用户名或密码")
	default:
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		return fmt.Errorf("WebDAV 创建同步目录失败（HTTP %d）", resp.StatusCode)
	}
}

func (c WebDAVConfig) headExists(ctx context.Context, client *http.Client, targetURL string) (bool, error) {
	resp, err := c.do(ctx, client, http.MethodHead, targetURL, nil, "")
	if err != nil {
		return false, err
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return false, errors.New("WebDAV 认证失败，请检查用户名或密码")
	}
	return false, nil
}

func (c WebDAVConfig) getBytes(ctx context.Context, client *http.Client, targetURL string) ([]byte, bool, error) {
	resp, err := c.do(ctx, client, http.MethodGet, targetURL, nil, "")
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, false, errors.New("WebDAV 认证失败，请检查用户名或密码")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false, fmt.Errorf("WebDAV 下载失败（HTTP %d）", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	return data, true, err
}

func (c WebDAVConfig) putBytes(ctx context.Context, client *http.Client, targetURL string, data []byte, contentType string) error {
	resp, err := c.do(ctx, client, http.MethodPut, targetURL, data, contentType)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return errors.New("WebDAV 认证失败，请检查用户名或密码")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("WebDAV 上传失败（HTTP %d）", resp.StatusCode)
	}
	return nil
}

// TestWebDAV verifies connectivity and credentials against the configured server.
func TestWebDAV(ctx context.Context, cfg WebDAVConfig) error {
	base, client, err := cfg.normalize()
	if err != nil {
		return err
	}
	parentURL := strings.TrimSuffix(base, "/FolioSync")
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", strings.TrimRight(parentURL, "/")+"/", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Depth", "0")
	if cfg.Username != "" || cfg.Password != "" {
		req.SetBasicAuth(cfg.Username, cfg.Password)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("无法连接 WebDAV 服务器：%w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented {
		// Fall back to OPTIONS when servers disable PROPFIND.
		optResp, optErr := cfg.do(ctx, client, http.MethodOptions, strings.TrimRight(parentURL, "/")+"/", nil, "")
		if optErr != nil {
			return fmt.Errorf("无法连接 WebDAV 服务器：%w", optErr)
		}
		_ = optResp.Body.Close()
		resp = optResp
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return errors.New("WebDAV 认证失败，请检查用户名或密码")
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("WebDAV 服务返回异常状态（HTTP %d）", resp.StatusCode)
	}
	return nil
}

// SyncWebDAV performs bidirectional shard-level sync for books, drawings, media, and PDFs.
func (s *Store) SyncWebDAV(ctx context.Context, cfg WebDAVConfig) (WebDAVSyncResult, error) {
	if s == nil {
		return WebDAVSyncResult{}, errors.New("本地存储未初始化")
	}
	base, client, err := cfg.normalize()
	if err != nil {
		return WebDAVSyncResult{}, err
	}
	for _, dir := range []string{
		base,
		base + "/books",
		base + "/drawings",
		base + "/media",
		base + "/documents",
	} {
		if err := cfg.ensureDir(ctx, client, dir); err != nil {
			return WebDAVSyncResult{}, err
		}
	}

	s.mu.Lock()
	localLib, err := s.read()
	s.mu.Unlock()
	if err != nil {
		return WebDAVSyncResult{}, err
	}

	var remoteManifest webdavManifest
	if data, found, err := cfg.getBytes(ctx, client, base+"/sync-manifest.json"); err != nil {
		return WebDAVSyncResult{}, err
	} else if found {
		_ = json.Unmarshal(data, &remoteManifest)
	}
	if remoteManifest.Books == nil {
		remoteManifest.Books = map[string]webdavBookMeta{}
	}
	if remoteManifest.Drawings == nil {
		remoteManifest.Drawings = map[string]string{}
	}

	var remoteIdx libraryIndex
	if data, found, err := cfg.getBytes(ctx, client, base+"/index.json"); err != nil {
		return WebDAVSyncResult{}, err
	} else if found {
		_ = json.Unmarshal(data, &remoteIdx)
	}

	localByID := make(map[string]Book, len(localLib.Books))
	localOrder := make([]string, 0, len(localLib.Books))
	for _, b := range localLib.Books {
		localByID[b.ID] = b
		localOrder = append(localOrder, b.ID)
	}
	remoteByID := make(map[string]Book)
	for _, entry := range remoteIdx.Books {
		fileName := filepath.Base(entry.File)
		if fileName == "" || fileName == "." {
			fileName = bookFileName(entry.ID)
		}
		data, found, err := cfg.getBytes(ctx, client, base+"/books/"+fileName)
		if err != nil {
			return WebDAVSyncResult{}, err
		}
		if !found {
			continue
		}
		var rb Book
		if err := json.Unmarshal(data, &rb); err != nil {
			continue
		}
		remoteByID[rb.ID] = rb
		if _, exists := localByID[rb.ID]; !exists {
			localOrder = append(localOrder, rb.ID)
		}
	}

	var res WebDAVSyncResult
	mergedBooks := make([]Book, 0, len(localOrder))
	toUploadBooks := make([]Book, 0)
	dirtyLocalBooks := make(map[string]bool)

	for _, id := range localOrder {
		lb, hasLocal := localByID[id]
		rb, hasRemote := remoteByID[id]
		switch {
		case hasLocal && !hasRemote:
			mergedBooks = append(mergedBooks, lb)
			toUploadBooks = append(toUploadBooks, lb)
		case !hasLocal && hasRemote:
			mergedBooks = append(mergedBooks, rb)
			dirtyLocalBooks[id] = true
			res.DownloadedBooks++
		case hasLocal && hasRemote:
			merged, localChanged, remoteChanged := mergeBooksForSync(lb, rb)
			mergedBooks = append(mergedBooks, merged)
			if localChanged {
				dirtyLocalBooks[id] = true
				res.DownloadedBooks++
			}
			if remoteChanged {
				toUploadBooks = append(toUploadBooks, merged)
			}
		}
	}

	mergedLib := Library{Books: mergedBooks}
	if len(dirtyLocalBooks) > 0 {
		s.mu.Lock()
		writeErr := s.writeAllShards(mergedLib, dirtyLocalBooks)
		s.mu.Unlock()
		if writeErr != nil {
			return WebDAVSyncResult{}, writeErr
		}
	}

	// Sync referenced media & PDF documents.
	mediaSet := map[string]bool{}
	for name := range referencedMedia(mergedBooks) {
		if mediaName.MatchString(name) {
			mediaSet[name] = true
		}
	}
	docSet := map[string]bool{}
	for _, b := range mergedBooks {
		for _, doc := range b.Documents {
			if pdfID.MatchString(doc.ID) {
				docSet[doc.ID] = true
			}
		}
	}

	for name := range mediaSet {
		localPath := s.MediaPath(name)
		if localPath == "" {
			continue
		}
		remoteURL := base + "/media/" + name
		if _, err := os.Stat(localPath); errors.Is(err, os.ErrNotExist) {
			data, found, err := cfg.getBytes(ctx, client, remoteURL)
			if err != nil {
				return WebDAVSyncResult{}, err
			}
			if found {
				if err := os.MkdirAll(s.mediaDir(), 0700); err != nil {
					return WebDAVSyncResult{}, err
				}
				if _, err := writeImmutableFile(localPath, data); err != nil {
					return WebDAVSyncResult{}, err
				}
				res.DownloadedFiles++
			}
			continue
		}
		exists, err := cfg.headExists(ctx, client, remoteURL)
		if err != nil {
			return WebDAVSyncResult{}, err
		}
		if !exists {
			data, err := os.ReadFile(localPath)
			if err == nil {
				if err := cfg.putBytes(ctx, client, remoteURL, data, "application/octet-stream"); err != nil {
					return WebDAVSyncResult{}, err
				}
				res.UploadedFiles++
			}
		}
	}

	for docID := range docSet {
		localPath := s.DocumentPath(docID)
		if localPath == "" {
			continue
		}
		remoteURL := base + "/documents/" + docID + ".pdf"
		if _, err := os.Stat(localPath); errors.Is(err, os.ErrNotExist) {
			data, found, err := cfg.getBytes(ctx, client, remoteURL)
			if err != nil {
				return WebDAVSyncResult{}, err
			}
			if found {
				if err := os.MkdirAll(filepath.Join(s.dir, "documents"), 0700); err != nil {
					return WebDAVSyncResult{}, err
				}
				if _, err := writeImmutableFile(localPath, data); err != nil {
					return WebDAVSyncResult{}, err
				}
				res.DownloadedFiles++
			}
			continue
		}
		exists, err := cfg.headExists(ctx, client, remoteURL)
		if err != nil {
			return WebDAVSyncResult{}, err
		}
		if !exists {
			data, err := os.ReadFile(localPath)
			if err == nil {
				if err := cfg.putBytes(ctx, client, remoteURL, data, "application/pdf"); err != nil {
					return WebDAVSyncResult{}, err
				}
				res.UploadedFiles++
			}
		}
	}

	// Sync per-book drawings by content hash & richness.
	newDrawingHashes := make(map[string]string, len(mergedBooks))
	for _, b := range mergedBooks {
		localDrawing, err := s.ReadDrawing(b.ID)
		if err != nil {
			localDrawing = Drawing{}
		}
		localData, _ := json.Marshal(localDrawing)
		localHash := sha256Hex(localData)
		fileKey := drawingFileName(b.ID)
		remoteURL := base + "/drawings/" + fileKey

		remoteData, found, err := cfg.getBytes(ctx, client, remoteURL)
		if err != nil {
			return WebDAVSyncResult{}, err
		}
		if !found {
			if len(localDrawing.Ink) > 0 || len(localDrawing.Pages) > 0 || len(localDrawing.Cards) > 0 {
				if err := cfg.putBytes(ctx, client, remoteURL, localData, "application/json"); err != nil {
					return WebDAVSyncResult{}, err
				}
				res.UploadedFiles++
			}
			newDrawingHashes[fileKey] = localHash
			continue
		}

		var remoteDrawing Drawing
		if err := json.Unmarshal(remoteData, &remoteDrawing); err != nil {
			newDrawingHashes[fileKey] = localHash
			continue
		}
		remoteHash := sha256Hex(remoteData)
		if remoteHash == localHash {
			newDrawingHashes[fileKey] = localHash
			continue
		}

		localEmpty := len(localDrawing.Ink) == 0 && len(localDrawing.Pages) == 0 && len(localDrawing.Cards) == 0
		remoteEmpty := len(remoteDrawing.Ink) == 0 && len(remoteDrawing.Pages) == 0 && len(remoteDrawing.Cards) == 0
		prevHash := remoteManifest.Drawings[fileKey]

		switch {
		case localEmpty && !remoteEmpty:
			if err := s.SaveDrawing(b.ID, remoteDrawing); err != nil {
				return WebDAVSyncResult{}, err
			}
			res.DownloadedFiles++
			newDrawingHashes[fileKey] = remoteHash
		case remoteEmpty && !localEmpty:
			if err := cfg.putBytes(ctx, client, remoteURL, localData, "application/json"); err != nil {
				return WebDAVSyncResult{}, err
			}
			res.UploadedFiles++
			newDrawingHashes[fileKey] = localHash
		case prevHash == localHash && prevHash != remoteHash:
			// Only remote changed since last sync.
			if err := s.SaveDrawing(b.ID, remoteDrawing); err != nil {
				return WebDAVSyncResult{}, err
			}
			res.DownloadedFiles++
			newDrawingHashes[fileKey] = remoteHash
		case prevHash == remoteHash && prevHash != localHash:
			// Only local changed since last sync.
			if err := cfg.putBytes(ctx, client, remoteURL, localData, "application/json"); err != nil {
				return WebDAVSyncResult{}, err
			}
			res.UploadedFiles++
			newDrawingHashes[fileKey] = localHash
		default:
			mergedDrawing := mergeDrawingsForSync(localDrawing, remoteDrawing)
			mergedData, _ := json.Marshal(mergedDrawing)
			if err := s.SaveDrawing(b.ID, mergedDrawing); err != nil {
				return WebDAVSyncResult{}, err
			}
			if err := cfg.putBytes(ctx, client, remoteURL, mergedData, "application/json"); err != nil {
				return WebDAVSyncResult{}, err
			}
			res.UploadedFiles++
			newDrawingHashes[fileKey] = sha256Hex(mergedData)
		}
	}

	for _, b := range toUploadBooks {
		data, err := json.Marshal(b)
		if err != nil {
			return WebDAVSyncResult{}, err
		}
		if err := cfg.putBytes(ctx, client, base+"/books/"+bookFileName(b.ID), data, "application/json"); err != nil {
			return WebDAVSyncResult{}, err
		}
		res.UploadedBooks++
	}

	now := time.Now().Unix()
	idxBytes, err := json.Marshal(makeIndex(mergedBooks))
	if err != nil {
		return WebDAVSyncResult{}, err
	}
	if len(toUploadBooks) > 0 || len(dirtyLocalBooks) > 0 || len(remoteIdx.Books) != len(mergedBooks) {
		if err := cfg.putBytes(ctx, client, base+"/index.json", idxBytes, "application/json"); err != nil {
			return WebDAVSyncResult{}, err
		}
	}

	nextManifest := webdavManifest{
		Version:   1,
		UpdatedAt: now,
		Books:     make(map[string]webdavBookMeta, len(mergedBooks)),
		Drawings:  newDrawingHashes,
	}
	for _, b := range mergedBooks {
		nextManifest.Books[b.ID] = webdavBookMeta{
			ID:        b.ID,
			Title:     b.Title,
			UpdatedAt: bookMaxUpdatedAt(b),
		}
	}
	manifestBytes, err := json.MarshalIndent(nextManifest, "", "  ")
	if err != nil {
		return WebDAVSyncResult{}, err
	}
	if err := cfg.putBytes(ctx, client, base+"/sync-manifest.json", manifestBytes, "application/json"); err != nil {
		return WebDAVSyncResult{}, err
	}

	res.Library = mergedLib
	res.SyncedAt = now
	return res, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func drawingFileName(bookID string) string {
	if topic := bookTopicID(bookID); topic != "" {
		bookID = "topic:" + topic
	}
	return fmt.Sprintf("%x.json", sha256.Sum256([]byte(bookID)))
}

func bookMaxUpdatedAt(b Book) int64 {
	var best int64
	for _, n := range b.Notes {
		if n.UpdatedAt > best {
			best = n.UpdatedAt
		}
	}
	return best
}

func mergeBooksForSync(local, remote Book) (merged Book, localChanged, remoteChanged bool) {
	lbBytes, _ := json.Marshal(local)
	rbBytes, _ := json.Marshal(remote)
	if bytes.Equal(lbBytes, rbBytes) {
		return local, false, false
	}

	merged = local
	if bookMaxUpdatedAt(remote) > bookMaxUpdatedAt(local) && remote.Title != "" {
		merged.Title = remote.Title
	}
	merged.Documents, _ = mergeDocuments(local.Documents, remote.Documents)
	merged.PDFsScanned = local.PDFsScanned || remote.PDFsScanned

	localMap := make(map[string]Note, len(local.Notes))
	order := make([]string, 0, len(local.Notes))
	for _, n := range local.Notes {
		localMap[n.ID] = n
		order = append(order, n.ID)
	}
	remoteMap := make(map[string]Note, len(remote.Notes))
	for _, n := range remote.Notes {
		remoteMap[n.ID] = n
		if _, ok := localMap[n.ID]; !ok {
			order = append(order, n.ID)
		}
	}

	mergedNotes := make([]Note, 0, len(order))
	for _, id := range order {
		ln, hasL := localMap[id]
		rn, hasR := remoteMap[id]
		switch {
		case hasL && !hasR:
			mergedNotes = append(mergedNotes, ln)
		case !hasL && hasR:
			mergedNotes = append(mergedNotes, rn)
		default:
			winner := ln
			loser := rn
			if rn.UpdatedAt > ln.UpdatedAt {
				winner = rn
				loser = ln
			} else if rn.UpdatedAt == ln.UpdatedAt {
				// Prefer edited note on timestamp ties.
				lScore := boolScore(ln.TitleEdited) + boolScore(ln.BodyEdited) + boolScore(ln.MetaEdited)
				rScore := boolScore(rn.TitleEdited) + boolScore(rn.BodyEdited) + boolScore(rn.MetaEdited)
				if rScore > lScore {
					winner = rn
					loser = ln
				}
			}
			for _, childID := range loser.Children {
				if !slices.Contains(winner.Children, childID) {
					winner.Children = append(winner.Children, childID)
				}
			}
			for _, tag := range loser.Tags {
				if !slices.Contains(winner.Tags, tag) {
					winner.Tags = append(winner.Tags, tag)
				}
			}
			winner.Tags = NormalizeTags(winner.Tags)
			if len(winner.MaskRects) == 0 && len(loser.MaskRects) > 0 {
				winner.MaskRects = slices.Clone(loser.MaskRects)
			}
			mergedNotes = append(mergedNotes, winner)
		}
	}
	merged.Notes = mergedNotes
	for _, rootID := range remote.Roots {
		if !slices.Contains(merged.Roots, rootID) {
			merged.Roots = append(merged.Roots, rootID)
		}
	}

	mergedBytes, _ := json.Marshal(merged)
	localChanged = !bytes.Equal(lbBytes, mergedBytes)
	remoteChanged = !bytes.Equal(rbBytes, mergedBytes)
	return merged, localChanged, remoteChanged
}

func boolScore(v bool) int {
	if v {
		return 1
	}
	return 0
}

func mergeDrawingsForSync(local, remote Drawing) Drawing {
	out := Drawing{
		Ink:   slices.Clone(local.Ink),
		Pages: map[string][]Stroke{},
		Cards: slices.Clone(local.Cards),
	}
	if len(remote.Ink) > len(out.Ink) {
		out.Ink = slices.Clone(remote.Ink)
	}
	for k, v := range local.Pages {
		out.Pages[k] = slices.Clone(v)
	}
	for k, rv := range remote.Pages {
		if lv, ok := out.Pages[k]; !ok || len(rv) > len(lv) {
			out.Pages[k] = slices.Clone(rv)
		}
	}
	seenCards := make(map[string]bool, len(out.Cards))
	for _, c := range out.Cards {
		seenCards[c.ID] = true
	}
	for _, rc := range remote.Cards {
		if !seenCards[rc.ID] {
			out.Cards = append(out.Cards, rc)
		}
	}
	return out
}
