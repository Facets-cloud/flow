// Package connectors embeds the sample human connectors so a flow binary
// installed without a repo checkout can still `flow connector add <name>`.
// The folders next to this file stay the single source of truth; add a
// new sample folder to the go:embed line below.
package connectors

import "embed"

// Samples holds one directory per sample connector (e.g. "macos").
// embed drops file modes, so the installer restores the exec bit on each
// manifest's exec target.
//
//go:embed macos
var Samples embed.FS
