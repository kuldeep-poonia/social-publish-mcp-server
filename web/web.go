// Package web contains embedded static web assets for serving the public landing page, logo, and favicon.
package web

import (
	_ "embed"
)

// IndexHTML contains the embedded Apple-style landing page.
//
//go:embed index.html
var IndexHTML []byte

// LogoPNG contains the high-res 1024x1024 application logo image.
//
//go:embed logo.png
var LogoPNG []byte

// FaviconSVG contains the scalable vector icon.
//
//go:embed favicon.svg
var FaviconSVG []byte
