// Package web embeds the panel interface so the binary needs no files beside it.
package web

import "embed"

// FS holds the static panel files served by the Go version.
//
//go:embed index.html style.css app.js chat.js models.js server.js settings.js favicon.svg brand-light.svg brand-dark.svg inter-400.woff.base64 inter-600.woff.base64
//go:embed manifest.webmanifest sw.js icon-192.png icon-512.png icon-maskable-512.png apple-touch-icon.png favicon-32.png
//go:embed vendor/katex.min.js vendor/highlight.min.js vendor/mermaid.min.js vendor/LICENSES.txt
var FS embed.FS
