package main

import (
	"embed"

	"github.com/egoist/mygo/ui"
)

//go:embed resources/figma/*.svg
var figmaAssets embed.FS

func designIcon(name string) *ui.SVG {
	data, err := figmaAssets.ReadFile("resources/figma/" + name + ".svg")
	if err != nil {
		panic(err)
	}
	return ui.MustParseSVG(data)
}

var (
	icTransfer      = designIcon("transfer")
	icCollapse      = designIcon("sidebar-collapse")
	icExpand        = designIcon("sidebar-expand")
	icSettings      = designIcon("settings")
	icMargin        = designIcon("margin")
	icSettingImport = designIcon("import")
	icSettingExport = designIcon("export")
	icSettingDelete = designIcon("delete")
	icFigmaBrand    = designIcon("brand")
	toggleOn        = designIcon("toggle-on")
	toggleOnDark    = designIcon("toggle-on-dark")
	toggleOff       = designIcon("toggle-off")
)
