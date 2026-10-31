package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/getan/golder/internal/browserctl"
	"github.com/getan/kitty-browse/browserhost"
)

func TestBrowserNamedProfileRemembersLoginRuntime(t *testing.T) {
	chrome := os.Getenv("GOLDER_TEST_CHROMIUM_EXECUTABLE")
	if os.Getenv("GOLDER_TEST_BROWSER_PROFILE") != "1" || chrome == "" {
		t.Skip("set GOLDER_TEST_BROWSER_PROFILE=1 and GOLDER_TEST_CHROMIUM_EXECUTABLE")
	}
	t.Setenv("GOLDER_HOME", t.TempDir())
	root := t.TempDir()
	profile, err := persistentBrowserProfile(root, "publishing", true)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "fixture_login", Value: "fixture_only", Path: "/", MaxAge: 3600, HttpOnly: true})
			fmt.Fprint(w, `<title>Fixture login</title><body>signed in</body>`)
			return
		}
		state := "missing"
		if c, err := r.Cookie("fixture_login"); err == nil && c.Value == "fixture_only" {
			state = "remembered"
		}
		fmt.Fprintf(w, `<title>Fixture check</title><body><div id="state">%s</div></body>`, state)
	}))
	defer server.Close()
	dir, err := os.MkdirTemp("", "gpr-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	for _, path := range []string{"/login", "/check"} {
		h, err := browserhost.Start(ctx, browserhost.Options{
			URL: server.URL + path, Chrome: chrome, ProfileDir: profile,
			Socket:         filepath.Join(dir, "c.sock"),
			RequireSandbox: true, LoadTimeout: 5 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer h.Close()
			c, err := browserctl.Dial(filepath.Join(dir, "c.sock"))
			if err != nil {
				t.Fatal(err)
			}
			defer c.CloseIdleConnections()
			info, err := c.Info(ctx)
			if err != nil || info.Backend != "native-cdp" {
				t.Fatalf("browser runtime unavailable: %v", err)
			}
			if path == "/check" {
				raw, _, err := c.Action(ctx, "get", map[string]any{"kind": "text", "selector": "#state"}, browserctl.Target{}, time.Second)
				var state string
				if err != nil || json.Unmarshal(raw, &state) != nil || state != "remembered" {
					t.Fatalf("login did not survive browser restart: state=%q err=%v", state, err)
				}
			}
		}()
	}
	t.Log("dedicated profile reused a fixture login after full Chromium shutdown/restart; no cookie values exported")
}
