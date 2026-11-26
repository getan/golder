package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/getan/golder/internal/extensions"
)

func TestDocxPackageDetailOffersInstall(t *testing.T) {
	m := extensionModel(t)
	res, _ := m.chooseExtension(extensionValue("package-detail", "docx"))
	m = res.(Model)
	for _, row := range m.menu.pick {
		if row.Value == extensionValue("docx-install") {
			return
		}
	}
	t.Fatalf("DOCX package detail has no install action: %+v", m.menu.pick)
}

func installDocxTestPackage(t *testing.T, version string) extensions.ManagedPackage {
	t.Helper()
	source := filepath.Join(t.TempDir(), "golder-docx")
	body := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(source, body, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	pkg, err := extensions.InstallManagedPackage(extensions.PackageInstallOptions{
		ID: "docx", Version: version, Source: source, SHA256: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func setDocxTestSource(t *testing.T, m Model, pkg extensions.ManagedPackage) {
	t.Helper()
	cfg := extensions.SourceConfig{
		Enabled: false, Transport: "stdio", Command: pkg.Executable,
		Args:         []string{"serve"},
		AllowedTools: []string{"docx_read", "docx_create", "docx_edit"},
		CallTimeout:  "30s",
	}
	if err := m.opts.Extensions.ReplaceSourceConfig(docxSourceRef, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestDocxPackageDetailOffersUpdate(t *testing.T) {
	m := extensionModel(t)
	m.opts.Version = "1.5.164"
	installDocxTestPackage(t, "1.0.0")
	res, _ := m.chooseExtension(extensionValue("package-detail", "docx"))
	m = res.(Model)
	for _, row := range m.menu.pick {
		if row.Value != extensionValue("docx-update") {
			continue
		}
		if !strings.Contains(row.Title, "1.0.0") || !strings.Contains(row.Title, extensions.DocxHelperVersion()) {
			t.Fatalf("update title = %q", row.Title)
		}
		return
	}
	t.Fatalf("DOCX package detail has no update action: %+v", m.menu.pick)
}

func TestDocxSourceDetailOffersHelperUpdate(t *testing.T) {
	m := extensionModel(t)
	m.opts.Version = "1.5.164"
	pkg := installDocxTestPackage(t, "1.0.0")
	setDocxTestSource(t, m, pkg)
	res, _ := m.extensionDetailPage(docxSourceRef, "", false)
	m = res.(Model)
	for _, row := range m.menu.pick {
		if row.Value != extensionValue("docx-helper-update") {
			continue
		}
		if !strings.Contains(row.Title, "1.0.0") || !strings.Contains(row.Title, extensions.DocxHelperVersion()) {
			t.Fatalf("helper update title = %q", row.Title)
		}
		return
	}
	t.Fatalf("DOCX source detail has no helper update action: %+v", m.menu.pick)
}

func TestDocxHelperUpdateWizardStartsAtConfirmation(t *testing.T) {
	m := extensionModel(t)
	m.opts.Version = "1.5.164"
	installDocxTestPackage(t, "1.0.0")
	res, _ := m.beginDocxUpdate()
	m = res.(Model)
	if m.docxWizard == nil || m.docxWizard.phase != "confirm" || !m.docxWizard.update {
		t.Fatalf("wizard = %+v", m.docxWizard)
	}
	view := m.docxWizard.view()
	for _, want := range []string{"Update", "1.0.0", extensions.DocxHelperVersion()} {
		if !strings.Contains(view, want) {
			t.Fatalf("confirmation view missing %q: %s", want, view)
		}
	}
}

func TestDocxHelperUpdateRewritesAndEnablesSource(t *testing.T) {
	m := extensionModel(t)
	m.opts.Version = "1.5.164"
	oldPkg := installDocxTestPackage(t, "1.0.0")
	newPkg := installDocxTestPackage(t, extensions.DocxHelperVersion())
	setDocxTestSource(t, m, oldPkg)

	original := installDocxRelease
	t.Cleanup(func() { installDocxRelease = original })
	installDocxRelease = func(context.Context, string, func(extensions.DocxDownloadProgress)) (extensions.ManagedPackage, error) {
		return newPkg, nil
	}

	res, _ := m.beginDocxUpdate()
	m = res.(Model)
	res, cmd := m.startDocxInstall()
	m = res.(Model)
	for {
		msg, ok := cmd().(docxInstallEventMsg)
		if !ok {
			t.Fatal("unexpected install event")
		}
		res, next := m.finishDocxInstall(msg)
		m = res.(Model)
		if msg.done {
			break
		}
		cmd = next
	}
	if m.docxWizard == nil || m.docxWizard.phase != "done" {
		t.Fatalf("wizard = %+v", m.docxWizard)
	}
	cfg, err := m.opts.Extensions.Config(docxSourceRef)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ok := m.opts.Extensions.Source(docxSourceRef)
	if !ok || !snapshot.Enabled || cfg.Command != newPkg.Executable {
		t.Fatalf("source = %+v", cfg)
	}
	if !slices.Contains(cfg.AllowedTools, "docx_render") {
		t.Fatalf("allowed tools = %v", cfg.AllowedTools)
	}
}

func TestDocxInstallWizardStartsAtConfirmation(t *testing.T) {
	m := extensionModel(t)
	m.opts.Version = "1.5.159"
	res, _ := m.beginDocxInstall()
	m = res.(Model)
	if m.docxWizard == nil || m.docxWizard.phase != "confirm" {
		t.Fatalf("wizard = %+v", m.docxWizard)
	}
	view := m.docxWizard.view()
	if !strings.Contains(view, "DOCX") || !strings.Contains(view, "verified") {
		t.Fatalf("confirmation view = %q", view)
	}
}

func TestDocxInstallProgressAndErrorStayInWizard(t *testing.T) {
	events := make(chan tea.Msg)
	m := extensionModel(t)
	m.docxWizard = &docxInstallWizard{phase: "installing", events: events}
	res, _ := m.finishDocxInstall(docxInstallEventMsg{text: "Downloading DOCX helper… 1.0 MB"})
	m = res.(Model)
	if m.docxWizard == nil || m.docxWizard.phase != "installing" || !strings.Contains(m.docxWizard.progress, "1.0 MB") {
		t.Fatalf("progress wizard = %+v", m.docxWizard)
	}
	res, _ = m.finishDocxInstall(docxInstallEventMsg{done: true, err: errors.New("checksum mismatch")})
	m = res.(Model)
	if m.docxWizard == nil || m.docxWizard.phase != "error" || !strings.Contains(m.docxWizard.err, "checksum") {
		t.Fatalf("error wizard = %+v", m.docxWizard)
	}
}
