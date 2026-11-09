//go:build !windows

package mygoui

func dismissOwnedFileDialog(uintptr) bool { return false }
