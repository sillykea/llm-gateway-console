// Package web holds the built single-page app (web/dist), embedded into the
// console binary so a release is one file. Build it with `make web`.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// UI returns the built app, or nil if it hasn't been built.
func UI() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	return sub
}
