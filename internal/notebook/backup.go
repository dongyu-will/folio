package notebook

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const backupManifestFile = "folio-backup-manifest.json"

type backupManifest struct {
	Version    int   `json:"version"`
	ExportedAt int64 `json:"exportedAt"`
	BookCount  int   `json:"bookCount"`
}

// ExportBackupZIP writes a full self-contained archive of the library index,
// book shards, normalized drawings, settings, media attachments, and PDFs.
func (s *Store) ExportBackupZIP(out io.Writer) error {
	if s == nil {
		return errors.New("本地存储未初始化")
	}
	if out == nil {
		return errors.New("导出目标为空")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	s.drawingMu.Lock()
	defer s.drawingMu.Unlock()

	lib, err := s.read()
	if err != nil {
		return fmt.Errorf("读取笔记本库失败：%w", err)
	}
	idxData, err := json.Marshal(makeIndex(lib.Books))
	if err != nil {
		return err
	}
	manifestData, err := json.MarshalIndent(backupManifest{
		Version:    1,
		ExportedAt: time.Now().Unix(),
		BookCount:  len(lib.Books),
	}, "", "  ")
	if err != nil {
		return err
	}

	zw := zip.NewWriter(out)
	writeBytes := func(name string, data []byte, method uint16) error {
		hdr := &zip.FileHeader{
			Name:     name,
			Method:   method,
			Modified: time.Now(),
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}

	if err := writeBytes(backupManifestFile, manifestData, zip.Deflate); err != nil {
		_ = zw.Close()
		return err
	}
	if err := writeBytes("index.json", idxData, zip.Deflate); err != nil {
		_ = zw.Close()
		return err
	}
	if settingsData, err := os.ReadFile(filepath.Join(s.dir, "settings.json")); err == nil {
		if err := writeBytes("settings.json", settingsData, zip.Deflate); err != nil {
			_ = zw.Close()
			return err
		}
	}

	for _, book := range lib.Books {
		data, err := json.Marshal(book)
		if err != nil {
			_ = zw.Close()
			return err
		}
		if err := writeBytes("books/"+bookFileName(book.ID), data, zip.Deflate); err != nil {
			_ = zw.Close()
			return err
		}
	}

	// Materialize drawings (replaying any uncheckpointed journal patches) into
	// normalized drawing files so restored backups do not depend on journals.
	drawingsDir := filepath.Join(s.dir, "drawings")
	if entries, err := os.ReadDir(drawingsDir); err == nil {
		seenDrawings := map[string]bool{}
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			var baseFile string
			if !entry.IsDir() && strings.HasSuffix(name, ".json") {
				baseFile = name
			} else if entry.IsDir() && strings.HasSuffix(name, ".journal") {
				baseFile = strings.TrimSuffix(name, ".journal") + ".json"
			} else {
				continue
			}
			if seenDrawings[baseFile] {
				continue
			}
			seenDrawings[baseFile] = true
			drawing, err := s.readDrawing(filepath.Join(drawingsDir, baseFile))
			if err != nil {
				_ = zw.Close()
				return fmt.Errorf("读取手写数据失败：%w", err)
			}
			data, err := json.Marshal(drawing)
			if err != nil {
				_ = zw.Close()
				return err
			}
			if err := writeBytes("drawings/"+baseFile, data, zip.Deflate); err != nil {
				_ = zw.Close()
				return err
			}
		}
	}

	copyDirToZip := func(diskDir, zipPrefix string, method uint16) error {
		entries, err := os.ReadDir(diskDir)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			full := filepath.Join(diskDir, entry.Name())
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			hdr := &zip.FileHeader{
				Name:     zipPrefix + "/" + entry.Name(),
				Method:   method,
				Modified: info.ModTime(),
			}
			w, err := zw.CreateHeader(hdr)
			if err != nil {
				return err
			}
			f, err := os.Open(full)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(w, f)
			closeErr := f.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return err
			}
		}
		return nil
	}

	if err := copyDirToZip(s.mediaDir(), "media", zip.Store); err != nil {
		_ = zw.Close()
		return err
	}
	if err := copyDirToZip(filepath.Join(s.dir, "documents"), "documents", zip.Store); err != nil {
		_ = zw.Close()
		return err
	}

	return zw.Close()
}

func validateBackupEntryName(raw string) (string, bool) {
	if raw == "" || strings.Contains(raw, "\\") || strings.Contains(raw, ":") || strings.HasPrefix(raw, "/") {
		return "", false
	}
	cleaned := path.Clean(raw)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") {
		return "", false
	}
	parts := strings.Split(cleaned, "/")
	switch len(parts) {
	case 1:
		return cleaned, cleaned == backupManifestFile || cleaned == "index.json" || cleaned == "settings.json"
	case 2:
		dir, file := parts[0], parts[1]
		if file == "" || file == "." || file == ".." || strings.HasPrefix(file, ".") {
			return "", false
		}
		switch dir {
		case "books", "drawings":
			return cleaned, strings.HasSuffix(strings.ToLower(file), ".json")
		case "media", "documents":
			return cleaned, true
		default:
			return "", false
		}
	default:
		return "", false
	}
}

// RestoreBackupZIP validates and atomically replaces the local store from a backup archive.
func (s *Store) RestoreBackupZIP(zipPath string) (Library, Settings, error) {
	if s == nil {
		return Library{}, Settings{}, errors.New("本地存储未初始化")
	}
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return Library{}, Settings{}, fmt.Errorf("无法打开备份文件：%w", err)
	}
	defer r.Close()

	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return Library{}, Settings{}, err
	}
	stageDir, err := os.MkdirTemp(s.dir, ".mgnote-restore-*")
	if err != nil {
		return Library{}, Settings{}, err
	}
	defer os.RemoveAll(stageDir)

	for _, sub := range []string{"books", "drawings", "media", "documents"} {
		if err := os.MkdirAll(filepath.Join(stageDir, sub), 0700); err != nil {
			return Library{}, Settings{}, err
		}
	}

	var hasIndex bool
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			cleanDir := strings.TrimSuffix(f.Name, "/")
			if cleanDir != "" && !slices.Contains([]string{"books", "drawings", "media", "documents"}, cleanDir) {
				return Library{}, Settings{}, fmt.Errorf("备份包含非法目录：%s", f.Name)
			}
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return Library{}, Settings{}, fmt.Errorf("备份包含不允许的符号链接：%s", f.Name)
		}
		rel, ok := validateBackupEntryName(f.Name)
		if !ok {
			return Library{}, Settings{}, fmt.Errorf("备份包含非法或不受支持的路径：%s", f.Name)
		}
		dst := filepath.Join(stageDir, filepath.FromSlash(rel))
		if !strings.HasPrefix(dst, stageDir+string(filepath.Separator)) {
			return Library{}, Settings{}, fmt.Errorf("备份路径越界：%s", f.Name)
		}

		rc, err := f.Open()
		if err != nil {
			return Library{}, Settings{}, err
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			_ = rc.Close()
			return Library{}, Settings{}, err
		}
		_, copyErr := io.Copy(out, rc)
		closeErr := errors.Join(out.Close(), rc.Close())
		if err := errors.Join(copyErr, closeErr); err != nil {
			return Library{}, Settings{}, err
		}
		if rel == "index.json" {
			hasIndex = true
		}
	}

	if !hasIndex {
		return Library{}, Settings{}, errors.New("备份文件缺少 index.json")
	}
	if manifestData, err := os.ReadFile(filepath.Join(stageDir, backupManifestFile)); err == nil {
		var m backupManifest
		if err := json.Unmarshal(manifestData, &m); err != nil || m.Version > 1 {
			return Library{}, Settings{}, errors.New("备份版本不受当前版本支持")
		}
	}
	idxData, err := os.ReadFile(filepath.Join(stageDir, "index.json"))
	if err != nil {
		return Library{}, Settings{}, fmt.Errorf("读取备份索引失败：%w", err)
	}
	var idx libraryIndex
	if err := json.Unmarshal(idxData, &idx); err != nil {
		return Library{}, Settings{}, fmt.Errorf("备份索引损坏：%w", err)
	}
	for _, entry := range idx.Books {
		bookPath := filepath.Join(stageDir, "books", filepath.Base(entry.File))
		data, err := os.ReadFile(bookPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Library{}, Settings{}, err
		}
		var b Book
		if err := json.Unmarshal(data, &b); err != nil {
			return Library{}, Settings{}, fmt.Errorf("备份笔记文件损坏：%w", err)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	s.drawingMu.Lock()
	defer s.drawingMu.Unlock()

	backupOldDir, err := os.MkdirTemp(s.dir, ".mgnote-old-*")
	if err != nil {
		return Library{}, Settings{}, err
	}
	defer os.RemoveAll(backupOldDir)

	swapTargets := []string{"index.json", "settings.json", "books", "drawings", "media", "documents"}
	for _, name := range swapTargets {
		dst := filepath.Join(s.dir, name)
		oldDst := filepath.Join(backupOldDir, name)
		if _, err := os.Stat(dst); err == nil {
			if err := os.Rename(dst, oldDst); err != nil {
				return Library{}, Settings{}, err
			}
		}
		src := filepath.Join(stageDir, name)
		if _, err := os.Stat(src); err == nil {
			if err := os.Rename(src, dst); err != nil {
				// Best-effort rollback of previous targets.
				for _, prev := range swapTargets {
					if _, serr := os.Stat(filepath.Join(backupOldDir, prev)); serr == nil {
						_ = os.RemoveAll(filepath.Join(s.dir, prev))
						_ = os.Rename(filepath.Join(backupOldDir, prev), filepath.Join(s.dir, prev))
					}
				}
				return Library{}, Settings{}, err
			}
		}
	}
	_ = os.Remove(s.legacyPath())
	s.drawingJournals = nil

	lib, err := s.read()
	if err != nil {
		return Library{}, Settings{}, err
	}
	var settings Settings
	if data, err := os.ReadFile(filepath.Join(s.dir, "settings.json")); err == nil {
		_ = json.Unmarshal(data, &settings)
	}
	return lib, settings, nil
}
