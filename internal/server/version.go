// Package server provides the HTTP server, routing, security middleware, and CORS configuration.
package server

// Version and Commit hold the build-time application version and git commit SHA.
// These variables are injected during binary compilation via ldflags.
var (
	Version = "1.0.0"
	Commit  = "a4c74dc"
)
