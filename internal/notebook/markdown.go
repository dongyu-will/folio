package notebook

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

func (book Book) Markdown() []byte {
	return book.markdown(nil)
}

func (book Book) markdown(imageLink func(string) string) []byte {
	var text strings.Builder
	addImage := func(ref string) {
		if imageLink != nil && ref != "" {
			if path := imageLink(ref); path != "" {
				fmt.Fprintf(&text, "![](%s)\n\n", path)
			}
		}
	}
	fmt.Fprintf(&text, "# %s\n\n", book.Title)
	indices := make(map[string]int, len(book.Notes))
	for i, note := range book.Notes {
		indices[note.ID] = i
	}
	seen := make(map[string]bool, len(book.Notes))
	stack := slices.Clone(book.Roots)
	slices.Reverse(stack)
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		i, exists := indices[id]
		if !exists || seen[id] {
			continue
		}
		seen[id] = true
		note := book.Notes[i]
		title := note.Title
		if title == "" {
			title = "未命名节点"
		}
		fmt.Fprintf(&text, "<a id=\"note-%d\"></a>\n\n## %s\n\n", i+1, title)
		if note.Excerpt != "" && note.Body != note.Excerpt {
			fmt.Fprintln(&text, note.Excerpt)
			fmt.Fprintln(&text)
		}
		addImage(note.Clip)
		useBlocks := note.Body == ""
		if imageLink != nil {
			useBlocks = note.BlocksCoverBody(note.Body)
		}
		if !useBlocks && note.Body != "" {
			fmt.Fprintln(&text, note.Body)
			fmt.Fprintln(&text)
		}
		for _, b := range note.Blocks {
			if useBlocks && strings.TrimSpace(b.Text) != "" {
				fmt.Fprintln(&text, b.Text)
				fmt.Fprintln(&text)
			}
			addImage(b.Image)
		}
		if len(note.Children) > 0 {
			for _, child := range note.Children {
				if j, ok := indices[child]; ok {
					label := book.Notes[j].Title
					if label == "" {
						label = "未命名节点"
					}
					label = strings.NewReplacer("[", "\\[", "]", "\\]", "\n", " ").Replace(label)
					fmt.Fprintf(&text, "- [%s](#note-%d)\n", label, j+1)
				}
			}
			fmt.Fprintln(&text)
		}
		for i := len(note.Children) - 1; i >= 0; i-- {
			stack = append(stack, note.Children[i])
		}
	}
	return []byte(text.String())
}

func (s *Store) ExportMarkdownZIP(bookID string, out io.Writer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	library, err := s.read()
	if err != nil {
		return err
	}
	i := indexBook(&library, bookID)
	if i < 0 {
		return errors.New("找不到此笔记本")
	}
	archive := zip.NewWriter(out)
	err = s.writeMarkdownZIP(archive, library.Books[i])
	return errors.Join(err, archive.Close())
}

func (s *Store) writeMarkdownZIP(archive *zip.Writer, book Book) error {
	images := make(map[string]bool)
	markdown := book.markdown(func(ref string) string {
		images[ref] = true
		return "images/" + ref
	})
	page, err := archive.Create("README.md")
	if err != nil {
		return err
	}
	if _, err := page.Write(markdown); err != nil {
		return err
	}
	var names []string
	for name := range images {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		path := s.MediaPath(name)
		if path == "" {
			return fmt.Errorf("无效的图片名称：%s", name)
		}
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("无法读取导出图片 %s：%w", name, err)
		}
		entry, err := archive.CreateHeader(&zip.FileHeader{Name: "images/" + name, Method: zip.Store})
		if err == nil {
			_, err = io.Copy(entry, file)
		}
		if err := errors.Join(err, file.Close()); err != nil {
			return err
		}
	}
	return nil
}
