//go:build darwin && cgo

package computeruse

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func macTestBundle(t *testing.T, root, relative, id, name string) string {
	t.Helper()
	path := filepath.Join(root, relative)
	contents := filepath.Join(path, "Contents")
	if err := os.MkdirAll(contents, 0o755); err != nil {
		t.Fatal(err)
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>%s</string><key>CFBundleName</key><string>%s</string><key>CFBundlePackageType</key><string>APPL</string></dict></plist>`, id, name)
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMacApplicationInventoryDisambiguatesAndRejectsForgedCandidates(t *testing.T) {
	root := t.TempDir()
	macTestBundle(t, root, "one/Calculator.app", "example.calculator.one", "Calculator")
	macTestBundle(t, root, "two/Calculator.app", "example.calculator.two", "Calculator")
	macTestBundle(t, root, "Terminal.app", "com.apple.terminal", "Terminal")
	catalog := NewMacWindowCatalog()
	t.Cleanup(func() { _ = catalog.Close() })
	apps, err := catalog.applicationsIn(context.Background(), "Calculator", []string{root})
	if err != nil || len(apps) != 2 || apps[0].ID == apps[1].ID || apps[0].Identity == apps[1].Identity {
		t.Fatalf("ambiguous application inventory = %+v, %v", apps, err)
	}
	forged := apps[0]
	forged.Detail = "/different/application"
	if err := catalog.LaunchApplication(context.Background(), forged); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("forged application candidate was accepted: %v", err)
	}
	blocked, err := catalog.applicationsIn(context.Background(), "Terminal", []string{root})
	if err != nil || len(blocked) != 0 {
		t.Fatalf("terminal application was offered: %+v, %v", blocked, err)
	}
}

func TestMacApplicationInventorySupportsCalculatorChineseName(t *testing.T) {
	root := t.TempDir()
	macTestBundle(t, root, "Calculator.app", "com.apple.calculator", "Calculator")
	catalog := NewMacWindowCatalog()
	t.Cleanup(func() { _ = catalog.Close() })
	apps, err := catalog.applicationsIn(context.Background(), "计算器", []string{root})
	if err != nil || len(apps) != 1 || apps[0].Name == "" || apps[0].Identity == "" {
		t.Fatalf("localized application inventory = %+v, %v", apps, err)
	}
}

func TestMacApplicationLaunchDoesNotStealFrontendFocus(t *testing.T) {
	path := "/System/Applications/Calculator.app"
	if got := macApplicationLaunchArgs(path); !slices.Equal(got, []string{"-g", "-a", path}) {
		t.Fatalf("open arguments = %q", got)
	}
}
