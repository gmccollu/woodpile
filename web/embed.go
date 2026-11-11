// Package web holds the built web UI so the server can embed it in the binary.
//
// Build it with `npm run build` in this directory. Until then dist/ holds only
// .keep, which is enough for the embed directive to compile.
package web

import "embed"

// Dist is the contents of web/dist. Serve it with fs.Sub(Dist, "dist").
//
//go:embed all:dist
var Dist embed.FS
