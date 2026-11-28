//go:build !windows

package agenttool

// normalizeShellOutput is a no-op outside Windows. Unix shells already emit
// the bytes the caller asked for, and rewriting them would hide real output.
func normalizeShellOutput(s string) string { return s }
