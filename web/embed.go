// Package web holds the static user interface (plain HTML/CSS/ES modules, no
// build step). It is embedded into the binary; -web-dir serves a copy from
// disk instead, which is handy when customising the design.
package web

import "embed"

// FS contains the UI files.
//
//go:embed index.html favicon.svg css js fonts
var FS embed.FS
