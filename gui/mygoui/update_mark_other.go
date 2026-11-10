//go:build !darwin && !windows

package mygoui

func markGUIInternetDownload(_, _ string) error { return nil }
