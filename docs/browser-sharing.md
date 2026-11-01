# Human and agent browser sharing

Available in **Golder v1.5.41**, with the published **kitty-browse v0.2.7**
dependency. A normal build uses the version pinned in `go.mod`; no local sibling
checkout or development Go workspace is required. Restart the Golder workspace
after upgrading so new panes use the updated browser implementation.

## Remembering a login

The default remains an isolated profile for a fresh controlled browser. To opt
in, ask the agent to open a named profile, for example:

```json
{"action":"open","profile":"publishing","url":"https://juejin.cn/"}
```

The profile is dedicated to Golder and scoped to this project. Its cookies and
storage remain under the private `$GOLDER_HOME/browser-profiles/<project-key>/`
directory, outside the checkout. Names are not filesystem paths. Personal Chrome
profiles are never imported. Explicit profiles are not subject to the temporary
workspace profile pruning routine. Chromium's own profile lock prevents two
processes from opening the same profile concurrently.

First use requires an **exact human confirmation**, using `/permissions ask`.
Automatic review and full-access mode do not manufacture that confirmation.
After approval, this agent session may reopen the same profile without asking
again; the person may return to `/permissions auto` after the initial consent.
A different agent session must receive its own confirmation, even if it
runs in another pane of the same workspace. Discovering a browser is not such an
approval. Unapproved agents cannot use the browser tool to read its DOM or take
screenshots either. An approved agent can reuse the already-running browser of
the same project/profile, rather than starting a competing Chromium process.

The person and an approved agent interact with the same browser, so a manual
login also applies to later agent navigation. Cookies stay in Chromium; they
are not exported as tool output or put into the model context.

## Footer and input

The bottom line distinguishes:

- `Manual input ready`: no automatic action is running.
- `Agent observing: snapshot · manual input ready`: read-only observation does
  not lock out the keyboard.
- `Agent operating: click · please wait`: a mutating action has the existing
  short input exclusion. Conflicting input produces a visible footer notice.
- `Agent finished: ... · manual input ready`: a short completion label prevents
  quick actions from becoming an invisible flash.
- `Automation paused: ... · manual input ready · Ctrl+G resume`: a verification,
  rate limit or explicit pause requires the person to finish handling the page.

There is no new takeover/lease/priority system. A pause stops **future** automatic
mutations; an action already in flight may finish. Its busy indicator remains
visible until it does. Manual page input does not implicitly resume automation.
The footer includes action names only, never the text being typed or credentials.

## Pause and explicit continuation

The browser latches a pause for recognized visible challenge components,
explicit verification/rate-limit text in visible dialogs or alerts, or HTTP 429
on the current document. An ordinary login form, hidden challenge, technical
article or unrelated image/analytics error is not by itself evidence of a block.
This is deliberately not a universal anti-bot detector.

While paused, observation and manual input still work. Native automatic page
mutations, including repeated clicks, reload and new-tab requests, return the
non-retryable `browser_paused` error **without being sent to the page**. The
browser remains open. A model must not create another browser or change network
routes to bypass the pause. Already-running website scripts (including a site's
own QR polling) are not suspended; closing that tab stops them.

```json
{"action":"status"}
{"action":"pause"}
{"action":"resume"}
```

`resume` requires an exact human confirmation, or the person can press **Ctrl+G**
in the browser pane. No earlier action is replayed. If the challenge is still
visible, the next automatic mutation pauses again. Disappearance of a challenge
alone never auto-resumes. A new browser process starts with fresh pause state.

## Local verification

In addition to both modules' normal unit/race checks:

```sh
GOWORK=off \
  GOLDER_TEST_BROWSER_PROFILE=1 \
  GOLDER_TEST_CHROMIUM_EXECUTABLE=/absolute/path/to/chrome-headless-shell \
  go test -race ./internal/agenttool -run 'Browser.*Profile|ExecutorDynamicOperator' -count=1
```

This uses a dummy persistent HTTP-only login cookie on a local fixture, fully
shuts Chromium down and restarts it before checking the logged-in page. Separate
real-browser tests cover visible/hidden verification UI, primary-document 429,
blocked submissions, explicit continuation, and the existing PTY manual-input
regression. No real website account or cookie is used.

### Remaining integration-test observation

With all opt-in Chromium integration tests enabled, the existing
`TestSessionSyncAndChromeHistoryIntegration` intermittently reports CDP
`Not attached to an active page (-32000)` on Back/Forward. The navigation code
and that test were not changed here. The unmodified v0.2.6 comparison passed
three runs, which is not enough to establish the cause of the intermittency.
This remains an unresolved validation item, not a claim that the entire
Chromium-enabled suite is green. No test was removed or timeout inflated to
hide it; the sharing/profile/intervention and real PTY acceptance tests pass.
