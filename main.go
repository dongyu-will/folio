package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/dongyu-will/folio/internal/androidopen"
	"github.com/dongyu-will/folio/internal/notebook"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func main() {
	mygo.App.SetName("Folio")
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}

	dir := os.Getenv("MGNOTE_DATA_DIR")
	if dir == "" {
		if appData, err := mygo.App.Path(mygo.PathAppData); err == nil {
			legacy := filepath.Join(appData, "MGNote")
			if info, statErr := os.Stat(legacy); statErr == nil && info.IsDir() {
				dir = legacy
			}
		}
	}
	if dir == "" {
		var err error
		dir, err = mygo.App.Path(mygo.PathUserData)
		if err != nil {
			log.Fatal(err)
		}
	}
	store := notebook.NewStore(dir)
	if runtime.GOOS == "darwin" {
		if resources, err := mygo.App.Path(mygo.PathResources); err == nil {
			for _, path := range []string{filepath.Join(resources, "mgnote-ink"), filepath.Join(resources, "darwin", "mgnote-ink")} {
				if _, err := os.Stat(path); err == nil {
					store.SetInkRenderer(path)
					break
				}
			}
		}
	}
	if os.Getenv("MYGO_GENERATE") == "" && runtime.GOOS != "android" {
		if downloads, err := mygo.App.Path(mygo.PathDownloads); err == nil {
			if err := store.RecoverMedia(downloads); err != nil {
				log.Print(err)
			}
		}
	}
	var initialBook string
	for _, path := range os.Args[1:] {
		if strings.HasPrefix(path, "-") {
			continue
		}
		result, err := store.Import(path)
		if err != nil {
			log.Print(err)
			continue
		}
		initialBook = result.BookID
	}

	app := &workspace{store: store}
	mygo.App.OnQuit(func() {
		if err := app.drawingWrites.flush(store); err != nil {
			log.Print(err)
		}
	})
	settings, err := store.ReadSettings()
	if err != nil {
		app.showError(err)
	}
	app.settings = settings
	library, err := store.Read()
	if err != nil {
		app.showError(err)
	}
	app.setLibrary(library, initialBook)
	if runtime.GOOS == "android" {
		mygo.Native(androidopen.PluginID).On("stylus", app.onAndroidStylus)
		mygo.Native(androidopen.PluginID).On("ready", func([]byte) {
			app.notifyDownloadsReady()
		})
	}
	mygo.App.OnOpenFile(func(path string) {
		app.enqueueImport(path)
	})
	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title: "Folio", Content: ui.View(app.view),
			Width: 1280, Height: 800, MinWidth: 560, MinHeight: 480,
			StateKey: "workspace", BackgroundColor: "light-dark(#f5f5f7, #0e0f12)",
		})
		app.setWindow(win)
		win.OnFileDrop(func(e *mygo.FileDropEvent) {
			app.enqueueImport(e.Paths...)
		})
		if runtime.GOOS == "android" {
			app.notifyDownloadsReady()
		} else {
			app.flushImports()
		}
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
