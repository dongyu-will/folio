package notebook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	LayoutCards   = "cards"
	LayoutBoard   = "board"
	LayoutOutline = "outline"
	LayoutCompact = "compact"
)

type Settings struct {
	SidebarCollapsed    bool    `json:"sidebarCollapsed"`
	Appearance          string  `json:"appearance,omitempty"`
	GraphZoom           int     `json:"graphZoom,omitempty"`
	GraphLayout         string  `json:"graphLayout,omitempty"`
	HideGrid            bool    `json:"hideGrid,omitempty"`
	PDFNight            bool    `json:"pdfNight"`
	PenTapToSelect      bool    `json:"penTapToSelect,omitempty"`
	LastUpdateCheckUnix int64   `json:"lastUpdateCheckUnix,omitempty"`
	LastBookID          string  `json:"lastBookId,omitempty"`
	SplitRatioH         float32 `json:"splitRatioH,omitempty"`
	SplitRatioV         float32 `json:"splitRatioV,omitempty"`
	PDFMarginOpen       bool    `json:"pdfMarginOpen,omitempty"`
	PDFMargin           float32 `json:"pdfMargin,omitempty"`
	WebDAVURL           string  `json:"webdavUrl,omitempty"`
	WebDAVUsername      string  `json:"webdavUsername,omitempty"`
	WebDAVLastSyncUnix  int64   `json:"webdavLastSyncUnix,omitempty"`
}

func (s Settings) LayoutMode() string {
	switch s.GraphLayout {
	case LayoutBoard, LayoutOutline, LayoutCompact, LayoutCards:
		return s.GraphLayout
	default:
		return LayoutCards
	}
}

func (s *Store) StorageBytes() int64 {
	var size int64
	_ = filepath.WalkDir(s.dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return nil
		}
		if info, err := entry.Info(); err == nil {
			size += info.Size()
		}
		return nil
	})
	return size
}

func (s *Store) ReadSettings() (Settings, error) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	var settings Settings
	data, err := os.ReadFile(filepath.Join(s.dir, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return Settings{}, fmt.Errorf("无法读取设置：%w", err)
	}
	return settings, nil
}

func (s *Store) SaveSettings(settings Settings) error {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	return WriteFile(filepath.Join(s.dir, "settings.json"), data)
}
