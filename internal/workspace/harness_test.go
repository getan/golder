package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestOwnedHarnessUsesThisExecutableWithoutExternalInstall(t *testing.T) {
	root := t.TempDir()
	foreign := filepath.Join(root, "foreign")
	t.Setenv("TUIOS_HARNESS_DIR", foreign)
	b := &Backend{Executable: filepath.Join(root, "a path", `golder-"test`)}
	dir, err := b.writeHarness(filepath.Join(root, "own"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		ID     string `toml:"id"`
		Resume struct {
			Argv []string `toml:"argv"`
		} `toml:"resume"`
	}
	path := filepath.Join(dir, "golder.toml")
	if _, err := toml.DecodeFile(path, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "golder" || !reflect.DeepEqual(got.Resume.Argv, []string{b.Executable, "--resume", "{session_id}"}) {
		t.Fatalf("own harness = %+v", got)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatal("harness is not a private file")
	}
	if _, err := os.Lstat(foreign); !os.IsNotExist(err) {
		t.Fatal("workspace touched the external host's harness directory")
	}
	if _, err := b.writeHarness(filepath.Join(root, "own")); err != nil {
		t.Fatalf("restarting could not refresh its owned manifest: %v", err)
	}
}
