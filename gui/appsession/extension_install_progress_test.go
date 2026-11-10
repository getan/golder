package appsession

import (
	"context"
	"testing"

	"github.com/getan/golder/internal/extensions"
)

func TestGUIOfficialCLIInstallForwardsProgressForBothBackends(t *testing.T) {
	old := installOfficialCLI
	t.Cleanup(func() { installOfficialCLI = old })
	installOfficialCLI = func(_ context.Context, kind extensions.OfficialCLIKind, progress func(extensions.OfficialCLIInstallProgress)) (extensions.OfficialCLI, error) {
		progress(extensions.OfficialCLIInstallProgress{Kind: kind, Phase: "downloading", Downloaded: 128, Total: 512})
		return extensions.OfficialCLI{Kind: kind, Executable: "/verified/" + string(kind)}, nil
	}
	for _, project := range []bool{false, true} {
		kind := extensions.OfficialLarkCLI
		if project {
			kind = extensions.OfficialProjectCLI
		}
		var got extensions.OfficialCLIInstallProgress
		c := &Controller{}
		path, err := c.InstallOfficialLarkCLIWithProgress(context.Background(), project, func(p extensions.OfficialCLIInstallProgress) { got = p })
		if err != nil || path != "/verified/"+string(kind) || got.Kind != kind || got.Downloaded != 128 || got.Total != 512 {
			t.Fatalf("progress/path lost for %s: %+v %q %v", kind, got, path, err)
		}
	}
}
