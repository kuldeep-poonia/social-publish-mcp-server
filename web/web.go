// Package web contains embedded static web assets for serving the public landing page.
package web

import (
	_ "embed"
)

// IndexHTML contains the embedded Apple-style landing page.
//
//go:embed index.html
var IndexHTML []byte
