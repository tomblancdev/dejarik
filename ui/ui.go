// Package ui embeds everything the panel needs: the stylesheet, the family
// mark, the three font subsets and htmx. Nothing is fetched at render time,
// so the page works from a lane with no egress and tells Google nothing
// about who opened it.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed static templates
var files embed.FS

// Static is the tree served under /static/.
var Static = must(fs.Sub(files, "static"))

// Templates are the page and its fragments.
var Templates = must(fs.Sub(files, "templates"))

func must(f fs.FS, err error) fs.FS {
	if err != nil {
		panic(err)
	}
	return f
}
