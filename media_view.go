package main

import (
	"container/list"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"

	"github.com/egoist/mygo/ui"
)

const thumbnailBudget = 64 << 20

var mediaThumbnailSizes = [...]int{256, 512, 1024}

type mediaThumbnailKey struct {
	ref  string
	side int
}

type mediaThumbnailEntry struct {
	key    mediaThumbnailKey
	bitmap *ui.Bitmap
	size   int64
}

type mediaThumbnailCache struct {
	entries map[mediaThumbnailKey]*list.Element
	recent  list.List
	bytes   int64
}

func (c *mediaThumbnailCache) get(key mediaThumbnailKey) (*ui.Bitmap, bool) {
	entry := c.entries[key]
	if entry == nil {
		return nil, false
	}
	c.recent.MoveToFront(entry)
	return entry.Value.(mediaThumbnailEntry).bitmap, true
}

func (c *mediaThumbnailCache) put(key mediaThumbnailKey, bitmap *ui.Bitmap, budget int64) {
	// Bound failed entries too, so missing images do not restart background work
	// on every frame or grow the cache without limit.
	size := int64(128 + len(key.ref))
	if bitmap != nil {
		w, h := bitmap.Size()
		size += int64(w) * int64(h) * 4
	}
	if old := c.entries[key]; old != nil {
		c.remove(old)
	}
	if size > budget {
		return
	}
	if c.entries == nil {
		c.entries = make(map[mediaThumbnailKey]*list.Element)
	}
	c.entries[key] = c.recent.PushFront(mediaThumbnailEntry{key, bitmap, size})
	c.bytes += size
	for c.bytes > budget {
		c.remove(c.recent.Back())
	}
}

func (c *mediaThumbnailCache) remove(element *list.Element) {
	entry := element.Value.(mediaThumbnailEntry)
	delete(c.entries, entry.key)
	c.recent.Remove(element)
	c.bytes -= entry.size
}

type mediaOriginal struct {
	ref    string
	bitmap *ui.Bitmap
	ready  bool
}

type mediaViewState struct {
	thumbnails      mediaThumbnailCache
	pending         map[mediaThumbnailKey]bool
	decoding        int
	generation      uint64
	original        mediaOriginal
	readingOriginal bool
}

func (a *workspace) clearMediaCache() {
	m := &a.media
	m.generation++
	m.thumbnails = mediaThumbnailCache{}
	m.pending = nil
	m.original = mediaOriginal{}
	a.mediaDims = nil
}

func (a *workspace) imageDims(ref string) (int, int) {
	if ref == "" || a.store == nil {
		return 0, 0
	}
	if dim, ok := a.mediaDims[ref]; ok {
		return dim.X, dim.Y
	}
	var dim image.Point
	if path := a.store.MediaPath(ref); path != "" {
		if file, err := os.Open(path); err == nil {
			cfg, _, err := image.DecodeConfig(file)
			_ = file.Close()
			if err == nil {
				dim = image.Pt(cfg.Width, cfg.Height)
			}
		}
	}
	if a.mediaDims == nil {
		a.mediaDims = make(map[string]image.Point)
	}
	a.mediaDims[ref] = dim
	return dim.X, dim.Y
}

func (a *workspace) paintMediaThumbnail(p *ui.Painter, rect ui.Rect, ref string) {
	visible := intersect(rect, p.Visible())
	if visible.W <= 0 || visible.H <= 0 {
		return
	}
	pixels := max(rect.W, rect.H) * p.Scale()
	for i, side := range mediaThumbnailSizes {
		if pixels <= float32(side) || i == len(mediaThumbnailSizes)-1 {
			bitmap, settled := a.mediaThumbnailState(ref, i)
			if bitmap != nil {
				p.Image(bitmap, rect, ui.Contain)
				return
			}
			if settled {
				p.Fill(rect, ui.RGBA(140, 140, 148, .12), 6)
				pad := clamp(min(rect.W, rect.H)*.28, 6, 18)
				cx, cy := rect.X+rect.W/2, rect.Y+rect.H/2
				col := ui.RGBA(140, 140, 148, .42)
				p.Line(cx-pad/2, cy-pad/2, cx+pad/2, cy+pad/2, 1.5, col)
				p.Line(cx-pad/2, cy+pad/2, cx+pad/2, cy-pad/2, 1.5, col)
			} else {
				p.Fill(rect, ui.RGBA(128, 128, 136, .08), 6)
			}
			return
		}
	}
}

func (a *workspace) mediaThumbnail(ref string, tier int) *ui.Bitmap {
	bitmap, _ := a.mediaThumbnailState(ref, tier)
	return bitmap
}

func (a *workspace) mediaThumbnailState(ref string, tier int) (*ui.Bitmap, bool) {
	m := &a.media
	// A sharper cached thumbnail can also serve a smaller view.
	for _, side := range mediaThumbnailSizes[tier:] {
		if bitmap, ok := m.thumbnails.get(mediaThumbnailKey{ref, side}); ok {
			return bitmap, true
		}
	}
	key := mediaThumbnailKey{ref, mediaThumbnailSizes[tier]}
	if !m.pending[key] && m.decoding < 2 && a.store != nil {
		path := a.store.MediaPath(ref)
		if path == "" {
			return nil, true
		}
		win := a.window()
		if win == nil {
			bitmap := decodeMediaBitmap(path, key.side)
			a.updateMediaOrientation(ref, bitmap)
			m.thumbnails.put(key, bitmap, thumbnailBudget)
			return bitmap, true
		}
		if m.pending == nil {
			m.pending = make(map[mediaThumbnailKey]bool)
		}
		m.pending[key] = true
		m.decoding++
		generation := m.generation
		go func() {
			bitmap := decodeMediaBitmap(path, key.side)
			win.Update(func() {
				m.decoding--
				if m.generation == generation {
					delete(m.pending, key)
					a.updateMediaOrientation(ref, bitmap)
					m.thumbnails.put(key, bitmap, thumbnailBudget)
				}
			})
		}()
	}
	for i := tier - 1; i >= 0; i-- {
		if bitmap, _ := m.thumbnails.get(mediaThumbnailKey{ref, mediaThumbnailSizes[i]}); bitmap != nil {
			return bitmap, true
		}
	}
	_, settled := m.thumbnails.get(key)
	return nil, settled
}

func (a *workspace) updateMediaOrientation(ref string, bitmap *ui.Bitmap) {
	dim := a.mediaDims[ref]
	if bitmap != nil && dim.X > 0 && dim.Y > 0 {
		w, h := bitmap.Size()
		if (w > h && dim.X < dim.Y) || (w < h && dim.X > dim.Y) {
			a.mediaDims[ref] = image.Pt(dim.Y, dim.X)
		}
	}
}

func (a *workspace) originalMedia(ref string) (*ui.Bitmap, bool) {
	m := &a.media
	if m.original.ref != ref {
		m.original = mediaOriginal{ref: ref}
	}
	if m.original.ready || m.readingOriginal {
		return m.original.bitmap, m.original.ready
	}
	if a.store == nil {
		return nil, true
	}
	path := a.store.MediaPath(ref)
	win := a.window()
	if win == nil {
		m.original.bitmap, m.original.ready = decodeMediaBitmap(path, 0), true
		return m.original.bitmap, true
	}
	m.readingOriginal = true
	generation := m.generation
	go func() {
		bitmap := decodeMediaBitmap(path, 0)
		win.Update(func() {
			m.readingOriginal = false
			if m.generation == generation && a.preview == ref {
				m.original = mediaOriginal{ref, bitmap, true}
			}
		})
	}()
	return nil, false
}

func decodeMediaBitmap(path string, maxSide int) *ui.Bitmap {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if maxSide > 0 {
		bitmap, _ := ui.DecodeBitmapScaled(data, maxSide)
		return bitmap
	}
	bitmap, _ := ui.DecodeBitmap(data)
	return bitmap
}
