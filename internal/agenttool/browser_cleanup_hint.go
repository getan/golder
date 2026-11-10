package agenttool

// These are model workflow instructions, not new runtime authority. An exact
// force call still passes the normal pane ownership and authorization gates;
// closePane never turns a failed probe or wait into force on its own.
const tuiBrowserCloseGuide = " For an owned browser pane, prefer tuios close with its exact recorded window id; do not issue browser quit first. " +
	"Close requests graceful shutdown, waits for process exit, then removes the pane. Verify its result and that the pane is gone. " +
	"Never use force automatically except for the single exited-pane recovery below." + tuiBrowserExitedPaneRecovery

const tuiBrowserExitedPaneRecovery = " TUI-only exited-pane recovery: if browser quit was already sent prematurely, refresh browser list and confirm no live instance matches the recorded browserId or socket; the listing must not be truncated. " +
	"A missing socket alone, probe timeouts or permission errors do not prove absence. " +
	"Fresh tuios list must identify the same full window id as controlled=true, control_origin=agent-created and own=false. " +
	"Use tuios wait condition=window-exit for that exact id and require matched=true, then re-list and confirm no foreground process remains. " +
	"An empty foreground_cmd or idle output alone is not proof of exit. " +
	"Only after all checks succeed may you issue exactly one tuios close force:true for that exact id, solely to remove the exited pane. " +
	"Verify that the pane disappears; if it is already gone, do not force-close anything. " +
	"Do not repeat quit or retry force. Never apply this exception to an adopted, operator-claimed, user-owned, own/agent/approval pane, a live or paused browser, or an uncertain identity/exit; stop and report instead. " +
	"Removing an exited pane does not prove that shared Chromium or its profile has exited."
