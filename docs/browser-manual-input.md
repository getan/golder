# Browser pane manual input regression

## Scope and development status

Manual typing and the `browser fill` action use different paths. A successful
`fill` does not prove terminal keyboard input reaches the page.

The fixes below are in local `../kitty-browse` commit `ffd11dc`. Golder still pins
`github.com/getan/kitty-browse v0.2.6`; no dependency release, Golder release or
installed-binary replacement was performed. Use a development Go workspace
including both modules to exercise the fixes before publishing a dependency.
The local-only workspace for this verification is `.build/browser-input.go.work`.

## Reproduced failures

1. Clicking the page after editing the address bar left `Model.Editing` true.
   The page received the mouse click, but the toolbar retained keyboard input.
   A successful page mouse-down now cancels the address edit.
2. The keyboard gate inspected only `document.activeElement`. An input inside
   an open shadow root or same-origin iframe appeared as its non-editable host,
   so typed characters never reached the input. Focus detection and Escape's
   blur now follow the same nested active-element chain.
3. Any native action set `Bridge.Active`, including read-only `get` and `wait`.
   The pane discarded manual events while that flag was set. The real PTY
   test reproduced missing characters and lost clicks merely while observing
   the page. Input exclusion now counts mutating actions separately; the
   existing active-action indicator still includes read-only observation.

Mutating automation still excludes simultaneous manual input. This is not an
exclusive human-takeover mode. Cross-origin frames and closed shadow roots
are not covered by the nested-focus change.

## Verification

The dependency tests first failed on ordinary address-bar/page focus transfer
and on nested shadow-root/same-origin-frame inputs. After the fixes, the real
Chromium tests verify pointer focus, ASCII/CJK insertion, Backspace and blur.

Golder's `TestBrowserManualInputPTY` launches the actual `runBrowse` entry point
under a PTY against a local HTTP fixture. It writes terminal Ctrl+L, SGR mouse
press/release and UTF-8 input; the control API only reads results and requests
normal shutdown. It never uses `fill` to make the test pass. This also checks
typing while read-only observation is active.

```sh
# Set this to the locally installed test Chromium, not a downloaded-on-demand
# runtime. The verified local run used chrome-headless-shell.
export GOLDER_TEST_CHROMIUM_EXECUTABLE=/absolute/path/to/chrome-headless-shell

GOWORK="$PWD/.build/browser-input.go.work" \
  GOLDER_TEST_BROWSER_INPUT=1 \
  go test -race ./cmd/golder -run '^TestBrowserManualInputPTY$' -count=5 -timeout 60s

cd ../kitty-browse
GOWORK=off KITTY_BROWSE_CHROME="$GOLDER_TEST_CHROMIUM_EXECUTABLE" \
  go test -race ./internal/browser \
  -run 'TestEditableFocusNestedInputsIntegration|TestSessionDragAndTypingIntegration' \
  -count=1 -timeout 45s
GOWORK=off go test -race ./... -count=1 -timeout 2m
```

Without the opt-in variables the browser/PTY integration test explicitly skips;
an ordinary green unit suite is not evidence that manual pane input was tested.
