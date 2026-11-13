// Package web bundles the operator console.
//
// The console is a thin client: every decision still happens on the server, and
// nothing here can sign or execute an intent.
package web

import "embed"

// Files holds the static console assets.
//
//go:embed index.html app.js style.css
var Files embed.FS
