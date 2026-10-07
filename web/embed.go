// Package web embeds the dashboard UI and the agent installer so the server
// ships as one binary.
package web

import "embed"

//go:embed static
var Static embed.FS

//go:embed install.sh
var InstallScript []byte
