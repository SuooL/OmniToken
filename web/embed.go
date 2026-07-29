// Package web holds the panel frontend, shared by the server's embedded UI and
// by the desktop client (ADR-0008).
//
// It sits at the repository root rather than under internal/ so the desktop
// build can consume the same files. The embed lives here rather than in
// internal/server because go:embed cannot reach a parent directory.
package web

import "embed"

// FS serves the panel at the filesystem root: index.html, style.css and the
// view scripts sit directly in it, so callers can hand it to http.FileServerFS
// without an fs.Sub step.
//
// embed.go itself is not matched by these patterns.
//
// vendor/ holds prebuilt third-party libraries committed as-is (ADR-0010):
// no build step, and they ship inside the binary so the panel renders without
// reaching a CDN.
//
//go:embed index.html style.css *.js vendor/echarts.min.js
var FS embed.FS
