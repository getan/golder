package prompts

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/grants"
)

func TestNetworkListAndRemove(t *testing.T) {
	store, err := grants.NewNetwork(grants.Options{Path: filepath.Join(t.TempDir(), "network.json")})
	if err != nil {
		t.Fatal(err)
	}
	grants.SetProcessNetwork(store)
	defer grants.SetProcessNetwork(nil)
	if _, err := grants.AddNetworkHost("api.github.com", "webfetch", true, grants.Session, "test"); err != nil {
		t.Fatal(err)
	}
	if got := runNetworkList("en"); !strings.Contains(got, "api.github.com") || !strings.Contains(got, "webfetch") {
		t.Fatalf("list = %q", got)
	}
	if got := runNetworkRemove("host api.github.com", "en"); !strings.Contains(got, "removed network rule") {
		t.Fatalf("remove = %q", got)
	}
	if got := runNetworkList("en"); !strings.Contains(got, "no remembered network rules") {
		t.Fatalf("list after remove = %q", got)
	}
}
