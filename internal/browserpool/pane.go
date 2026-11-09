package browserpool

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/getan/kitty-browse/browsepane"
)

// SharedPane dispatches only launches carrying a trusted runtime conversation
// ID. Existing direct `golder browse` invocations keep their standalone mode.
func SharedPane(ctx context.Context, args []string, pane browsepane.Options) (bool, error) {
	values := map[string]string{}
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "-golder-session", "-golder-instance", "-golder-state-root", "-profile-dir", "-login-profile", "-proxy-server", "-control-socket", "-chrome":
			values[args[i]] = args[i+1]
			i++
		}
	}
	if values["-golder-session"] == "" {
		return false, nil
	}
	if values["-golder-state-root"] == "" {
		return true, errors.New("shared browser state root unavailable")
	}
	// This is a dedicated child with a scrubbed HOME, not the frontend process.
	if err := os.Setenv("GOLDER_HOME", values["-golder-state-root"]); err != nil {
		return true, err
	}
	opts, err := Options(values["-profile-dir"], values["-login-profile"], values["-proxy-server"])
	if err != nil {
		return true, err
	}
	if values["-chrome"] != "" {
		opts.Browser.Chrome = values["-chrome"]
	}
	startup, stop := context.WithTimeout(ctx, 45*time.Second)
	view, err := Open(startup, opts, values["-golder-session"], values["-golder-instance"], args[len(args)-1])
	stop()
	if err != nil {
		return true, err
	}
	if view.Socket != values["-control-socket"] {
		return true, errors.New("browser view socket differs from the reviewed launch")
	}
	return true, browsepane.RunSharedPane(ctx, pane, view)
}
