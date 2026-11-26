package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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
