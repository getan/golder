# Human and agent browser sharing

Named-profile sharing was introduced in **Golder v1.5.41** with
**kitty-browse v0.2.7**. The project-shared default and bounded component-cache
behavior below shipped in **Golder v1.5.42** with **kitty-browse v0.2.8**. A
normal release build uses the version pinned in `go.mod`; restart the Golder
workspace after upgrading so new panes use the updated browser implementation.

## Remembering a login

The default is a persistent **project-shared profile**. A browser opened without
`profile` always uses `$GOLDER_HOME/browser-profiles/<project-key>/default.profile`,
outside the checkout. Its cookies, storage, history and downloads survive
restarts, and the visible person plus agents working in this project use the
same live browser and profile. The default profile is intentionally available
without the separate named-profile consent prompt.

For a separate identity, ask the agent to open a named profile, for example:

```json
{"action":"open","profile":"publishing","url":"https://juejin.cn/"}
```

Named profiles live beside `default.profile` and are still project-scoped.
Names are not filesystem paths, and personal Chrome profiles are never imported.
Chromium's own profile lock prevents two processes from opening the same profile
concurrently; a second agent adopts the live default browser instead of starting
a competing process.

Each browser tool session pins the tab opened or explicitly selected by that
session. Screenshot, snapshot and other actions use that tab even if Chromium
later activates a restored internal page. If the pinned tab closes, the tool
fails rather than silently choosing another; select a tab from `browser.list`.
An explicitly identified `chrome://` tab may be closed only in an owned
browser, without granting navigation or inspection access to internal pages.

Chromium component auto-updates are disabled for these automation profiles, so
speech packs and on-device optimization models cannot silently add gigabytes.
On each profile open, kitty-browse removes only regenerable component,
HTTP/code/GPU/shader caches. Cookies, Local Storage, IndexedDB, history and
downloads are retained.

First use of a non-default named profile requires an **exact human confirmation**,
using `/permissions ask`.
Automatic review and full-access mode do not manufacture that confirmation.
After approval, this agent session may reopen the same profile without asking
again; the person may return to `/permissions auto` after the initial consent.
A different agent session must receive its own confirmation, even if it
runs in another pane of the same workspace. Discovering a browser is not such an
approval. Unapproved agents cannot use the browser tool to read its DOM or take
screenshots either. An approved agent can reuse the already-running browser of
the same project/profile, rather than starting a competing Chromium process.

The person and an approved agent interact with the same browser, so a manual
login in the controlled pane applies to later agent navigation. A fallback GUI
Chromium must use the same project profile, basic password store, mock macOS
keychain and browser proxy, and the controlled browser must fully exit first.
Opening the same profile directory with ordinary macOS Chromium defaults is not
equivalent: cookies may be present on disk but encrypted for the system Keychain
and unreadable to the controlled runtime. Close the GUI browser completely before
reopening the pane; never overlap two Chromium processes on one profile or import
a personal Chrome profile. `browser status` reports the authorized profile path,
runtime/cookie-store mode and actual versus configured proxy mode without opening
the cookie database. The actual route is `direct` or `proxy` when the current
session launched the browser, and honestly remains `unknown` after adopting an
endpoint whose launch route cannot be attested; the configured route is what the
next launch will use. Cookies stay in Chromium; their names and values are not
exported as tool output or put into the model context.

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
