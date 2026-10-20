// Package truehand embeds the Claude Code plugin and its marketplace manifest, so the binary
// can install itself into Claude Code without network access or a checkout of this repo.
package truehand

import "embed"

//go:embed all:plugin .claude-plugin/marketplace.json
var ClaudePlugin embed.FS
