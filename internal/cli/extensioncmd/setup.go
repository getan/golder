package extensioncmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	setupevent "github.com/getan/golder/extension/setup"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/extensions"
	qrcode "github.com/skip2/go-qrcode"
)

type setupDisplayKey struct{}

// WithSetupDisplay installs an operator-only, transient display. TUI adapters
// must never append its URL/QR contents to agent history or the session log.
func WithSetupDisplay(ctx context.Context, show func(string)) context.Context {
	return context.WithValue(ctx, setupDisplayKey{}, show)
}

func runSetup(ctx context.Context, m *extensions.Manager, ref extensions.SourceRef, variant string) error {
	show, _ := ctx.Value(setupDisplayKey{}).(func(string))
	if show == nil {
		show = func(text string) { fmt.Fprintln(os.Stderr, text) }
	}
	var closers []func()
	defer func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}()
	cwd, _ := os.Getwd()
	return m.SetupVariant(ctx, ref, variant, func(event setupevent.Event) error {
		switch event.Type {
		case "notice":
			show(event.Message)
		case "authorization":
			show("Opening a private QR authorization viewer… Confirm permissions in Feishu on your phone. Ctrl+C cancels.")
			closeViewer, err := agenttool.OpenAuthorizationQR(ctx, cwd, event)
			if err == nil {
				closers = append(closers, closeViewer)
				show("Scan the QR in the authorization browser pane with Feishu. The desktop page is offline and has no model control endpoint. Waiting for the official CLI to verify login…")
				return nil
			}
			qr, qrErr := qrcode.New(event.VerificationURL, qrcode.Medium)
			var text strings.Builder
			text.WriteString("Browser viewer unavailable; continue using this same official URL/QR. No installer was run.\n")
			text.WriteString(event.VerificationURL + "\n")
			if qrErr == nil {
				text.WriteString(qr.ToSmallString(false))
			}
			text.WriteString("\nWaiting for authorization; Ctrl+C cancels.")
			show(text.String())
		case "complete":
			show("Authorization received; finishing CLI verification and cleanup…")
		}
		return nil
	})
}
