// Package kempt holds the files embedded into the kempt binary: the web demo
// UI and the sample spreadsheets it offers.
package kempt

import (
	"embed"
	"io/fs"
)

//go:embed web/index.html web/app.js web/style.css
var webFS embed.FS

//go:embed testdata/fixtures/*.xlsx testdata/fixtures/*.csv
var samplesFS embed.FS

// Web returns the UI files (index.html, app.js, style.css) at the root.
func Web() fs.FS {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	return sub
}

// Samples returns the bundled sample spreadsheets at the root.
func Samples() fs.FS {
	sub, err := fs.Sub(samplesFS, "testdata/fixtures")
	if err != nil {
		panic(err)
	}
	return sub
}
