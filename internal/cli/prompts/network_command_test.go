package prompts

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/judge"
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

func TestNetworkAdd(t *testing.T) {
	store, err := grants.NewNetwork(grants.Options{Path: filepath.Join(t.TempDir(), "network.json")})
	if err != nil {
		t.Fatal(err)
	}
	grants.SetProcessNetwork(store)
	defer grants.SetProcessNetwork(nil)

	if got := runNetworkAdd("command go test", "en"); !strings.Contains(got, "added network rule") {
		t.Fatalf("add command = %q", got)
	}
	if got := runNetworkAdd("host api.github.com webfetch", "en"); !strings.Contains(got, "api.github.com") {
		t.Fatalf("add host = %q", got)
	}
	if got := runNetworkAdd("tool websearch", "en"); !strings.Contains(got, "websearch") {
		t.Fatalf("add tool = %q", got)
	}
	if got := runNetworkList("en"); !strings.Contains(got, "command go test") || !strings.Contains(got, "api.github.com") {
		t.Fatalf("list after add = %q", got)
	}
	if got := runNetworkAdd("bogus", "en"); !strings.Contains(got, "usage") {
		t.Fatalf("bad add = %q", got)
	}
}

func TestNetworkFollow(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	judge.SetNetworkFollow(false)
	defer judge.SetNetworkFollow(false)

	if got := runNetworkFollow("", "en"); !strings.Contains(got, "the default") {
		t.Fatalf("follow show before any choice = %q, want it to name the default", got)
	}
	if got := runNetworkFollow("off", "en"); !strings.Contains(got, "disabled") {
		t.Fatalf("follow off = %q", got)
	}
	if judge.NetworkFollow() {
		t.Fatal("follow off did not reach the judge")
	}
	cfg, err := config.LoadNetworkConfig()
	if err != nil || cfg.Follow == nil || *cfg.Follow {
		t.Fatalf("persisted config = %+v, %v", cfg, err)
	}
	if got := runNetworkFollow("show", "en"); !strings.Contains(got, "opted out") {
		t.Fatalf("follow show after off = %q", got)
	}
	if got := runNetworkFollow("on", "en"); !strings.Contains(got, "enabled") {
		t.Fatalf("follow on = %q", got)
	}
	if !judge.NetworkFollow() {
		t.Fatal("follow on did not reach the judge")
	}
	cfg, err = config.LoadNetworkConfig()
	if err != nil || cfg.Follow == nil || !*cfg.Follow {
		t.Fatalf("persisted config after on = %+v, %v", cfg, err)
	}
}
