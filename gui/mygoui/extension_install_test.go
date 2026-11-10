package mygoui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/extensions"
)

func TestOfficialCLIInstallProgressIsVisibleBesideButtons(t *testing.T) {
	for _, project := range []bool{false, true} {
		t.Run(map[bool]string{false: "lark", true: "meegle"}[project], func(t *testing.T) {
			a := testApp()
			a.extension.Installed = true
			s, _ := a.beginOfficialCLIInstall(project)
			defer s.cancel()
			for _, tc := range []struct {
				phase, want string
				done, total int64
			}{
				{"connecting", "Connecting to download server", 0, 0},
				{"downloading", "25% · 1.0 MiB / 4.0 MiB", 1 << 20, 4 << 20},
				{"downloading", "1.0 MiB received (total size unknown)", 1 << 20, 0},
				{"downloading", "100%", 4 << 20, 4 << 20},
				{"verifying", "Verifying checksum", 4 << 20, 4 << 20},
				{"extracting", "Extracting native executable", 4 << 20, 4 << 20},
				{"installing", "Installing verified binary", 4 << 20, 4 << 20},
				{"complete", "Discovering installed CLI paths", 4 << 20, 4 << 20},
			} {
				a.applyOfficialCLIProgress(s, extensions.OfficialCLIInstallProgress{Phase: tc.phase, Downloaded: tc.done, Total: tc.total})
				tt := ui.NewTester(a.larkExtensionCard, 1000, 900)
				if !tt.HasText(s.text()) || !strings.Contains(s.text(), tc.want) || !tt.HasText("Installing "+s.name()+"…") || !tt.HasText("Cancel download") {
					t.Fatalf("missing inline installation progress for %s: %q", tc.phase, tt.Texts())
				}
				// Downloading 100% must never be presented as an installed CLI.
				if !a.extensionBusy || s.finished || a.extensionEdit.LarkCLI != "" || a.extensionEdit.ProjectCLI != "" {
					t.Fatalf("transfer/stage event prematurely finished installation: %+v", s)
				}
			}
		})
	}
}

func TestOfficialCLIInstallRefreshesBothPathsWithoutSavingConfiguration(t *testing.T) {
	for _, project := range []bool{false, true} {
		t.Run(map[bool]string{false: "lark", true: "meegle"}[project], func(t *testing.T) {
			a := testApp()
			a.extensionEdit = appsession.LarkExtensionEdit{AppID: "edited-app", NewAppSecret: "unsaved-secret", ProjectHost: "project.example"}
			s, ctx := a.beginOfficialCLIInstall(project)
			path := "/managed/lark-cli"
			if project {
				path = "/managed/meegle"
			}
			a.finishOfficialCLIInstall(s, path, "/managed/lark-cli", "/managed/meegle", nil)
			if a.extensionEdit.LarkCLI != "/managed/lark-cli" || a.extensionEdit.ProjectCLI != "/managed/meegle" {
				t.Fatalf("both paths not refreshed: %+v", a.extensionEdit)
			}
			if a.extensionEdit.AppID != "edited-app" || a.extensionEdit.NewAppSecret != "unsaved-secret" || a.extensionEdit.ProjectHost != "project.example" || a.extension.Configured || a.extension.Enabled {
				t.Fatal("automatic path discovery changed or applied unrelated configuration")
			}
			if a.extensionBusy || !s.finished || s.failed || ctx.Err() == nil || !strings.Contains(s.text(), "review them and save") {
				t.Fatalf("installation did not settle: %+v", s)
			}
			if tt := ui.NewTester(a.officialCLIInstallStatus, 800, 200); tt.HasText("Cancel download") || !tt.HasText(s.message) {
				t.Fatalf("completion was not rendered or left cancellation visible: %q", tt.Texts())
			}
		})
	}
}

func TestOfficialCLIInstallPreservesOtherCustomPathAndEditsDuringDownload(t *testing.T) {
	for _, project := range []bool{false, true} {
		a := testApp()
		a.extensionEdit.LarkCLI, a.extensionEdit.ProjectCLI = "/custom/lark", "/custom/meegle"
		s, _ := a.beginOfficialCLIInstall(project)
		if project {
			a.extensionEdit.ProjectCLI = "/chosen-during-download/meegle"
		} else {
			a.extensionEdit.LarkCLI = "/chosen-during-download/lark"
		}
		wantLark, wantMeegle := a.extensionEdit.LarkCLI, a.extensionEdit.ProjectCLI
		a.finishOfficialCLIInstall(s, "/newly-installed", "/detected/lark", "/detected/meegle", nil)
		if a.extensionEdit.LarkCLI != wantLark || a.extensionEdit.ProjectCLI != wantMeegle {
			t.Fatalf("custom paths were overwritten: %+v", a.extensionEdit)
		}
	}
}

func TestOfficialCLIInstallFailureCancellationAndStaleUpdates(t *testing.T) {
	for _, installErr := range []error{errors.New("HTTP 503"), context.DeadlineExceeded, context.Canceled} {
		t.Run(installErr.Error(), func(t *testing.T) {
			a := testApp()
			a.extensionEdit.LarkCLI, a.extensionEdit.ProjectCLI = "/before/lark", "/before/meegle"
			s, ctx := a.beginOfficialCLIInstall(false)
			a.cancelOfficialCLIInstall()
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatal("cancel did not interrupt the installing context")
			}
			a.finishOfficialCLIInstall(s, "", "", "", installErr)
			if a.extensionBusy || !s.finished || s.message == "" || s.failed == errors.Is(installErr, context.Canceled) {
				t.Fatalf("failure/cancellation did not settle: %+v", s)
			}
			if errors.Is(installErr, context.DeadlineExceeded) && !strings.Contains(s.message, "timed out") {
				t.Fatal("timeout was not explained")
			}
			current, _ := a.beginOfficialCLIInstall(true)
			defer current.cancel()
			a.applyOfficialCLIProgress(s, extensions.OfficialCLIInstallProgress{Phase: "complete"})
			a.finishOfficialCLIInstall(s, "/stale/lark", "/stale/lark", "/stale/meegle", nil)
			if a.extensionCLIInstall != current || !a.extensionBusy || current.progress.Phase != "checking" || a.extensionEdit.LarkCLI != "/before/lark" || a.extensionEdit.ProjectCLI != "/before/meegle" {
				t.Fatal("stale callback overwrote another installation or its paths")
			}
		})
	}
}
