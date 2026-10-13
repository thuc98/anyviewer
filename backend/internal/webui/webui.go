package webui

import (
	"embed"
	"io/fs"
)

//go:embed dist
var bundled embed.FS

var Assets, _ = fs.Sub(bundled, "dist")
var Index, _ = fs.ReadFile(Assets, "index.html")
