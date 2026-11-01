// Package lark contains the static instruction assets distributed with the
// extension. Installing them is a local, explicit operator action, not an MCP call.
package lark

import "embed"

//go:embed skills
var Assets embed.FS
