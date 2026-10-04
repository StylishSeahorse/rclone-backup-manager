// Package web embeds the dashboard UI so the server ships as one binary.
package web

import "embed"

//go:embed static
var Static embed.FS
