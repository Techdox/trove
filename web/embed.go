// Package web embeds the Trove dashboard SPA so the server ships as a single
// binary. Assets live under public/ and are served as-is (no build step).
package web

import (
	"embed"
	"io/fs"
	"mime"
)

//go:embed all:public
var files embed.FS

func init() {
	// Go's default MIME table has no entry for .webmanifest, so the file
	// server would serve site.webmanifest as text/plain. Register the correct
	// type so browsers accept it as a web app manifest.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

// FS returns the SPA file tree rooted at the public directory, suitable for
// http.FileServerFS.
func FS() fs.FS {
	sub, err := fs.Sub(files, "public")
	if err != nil {
		// Unreachable: the path is a compile-time embed constant.
		panic(err)
	}
	return sub
}
