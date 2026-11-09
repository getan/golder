# Conversation browser binding

## Ownership

GUI and TUI conversations share one persistent project login profile, **not**
one foreground page. Each conversation view has its own socket, browser identity
and tab set, owned by `browserhost`. No frontend is allowed to select a different
conversation's view through a cached inventory row.

There are two authoritative levels, not two competing frontend selections:

1. `internal/browserbinding.Binding` owns the conversation's selected browser
   endpoint. `ConfigureBrowserSession` gives the same object to the controller
   and its Agent broker tools. Reconfiguring a turn preserves it; changing
   conversations replaces it.
2. That endpoint's host owns its foreground tab (`Info.ActiveTabID`). Both the
   Agent's default-target resolution and the GUI's surface projection use
   `browserctl.Foreground`. The TUI shared pane already reads and selects this
   same host foreground through its authenticated operator connection.

Operator input and Agent automation deliberately keep separate authorization
paths. Sharing an identity/selection does not transfer operator credentials,
disable pause, authorize a profile or bypass review.

## Selection semantics

- Manual GUI/TUI tab selection and Agent `tab_select` update the host foreground.
- The next Agent call without `tab` uses that foreground. A missing or unsupported
  foreground fails closed instead of substituting a safe background tab.
- Preparation pins the socket, browser, tab, target and page URL. Later manual
  selection never redirects the prepared operation. A changed browser binding
  before execution is refused; an action already dispatched retains its target.
- Explicit background targets, snapshots and screenshots do not select a tab or
  reveal a dismissed GUI drawer.
- Standalone externally supplied hosts without a conversation retain their
  explicit target pin: their global foreground may belong to another frontend.

## GUI projection

The GUI subscribes to binding snapshots. Successful explicit foreground operations
advance a reveal revision; ordinary discovery does not. Closing a drawer only
dismisses that revision, not the browser or its binding. Inventory is presentation
data, never a fallback selection. Clicking a row revalidates its conversation and
live browser identity before changing the binding.

The surface reads host foreground directly and publishes pixels, chrome and input
target as one frame. Capture checks selection before and after the screenshot;
obsolete frames are discarded. Mouse, keyboard and deferred resize work stay
pinned to the displayed target. The GUI never interprets a tool transcript as a
control command and does not maintain a second tab-selection/follow-event queue.

Switching conversations cancels the subscription and capture, removes the old
surface and inventory, and starts a fresh subscription. Every callback checks the
subscription identity, controller, conversation and binding revision. This also
rejects callbacks from a previous visit after an A → B → A switch.

## Lifecycle and failure

Binding notifications coalesce: observers read the latest snapshot, never replay
an event stream. Completion and cleanup use revision-checked compare-and-swap,
so late work cannot clear or reveal a replacement selection, even after A → B → A.
Discovery can restore a single existing view only before any selection/clear has
occurred. Losing a selected view never silently switches to another live view.

Host Info polling remains necessary for changes originating in another process
(including a TUI operator) and for observing socket loss. It is observation of
the authority, not synchronization between two local selections. A hidden or
closed drawer is not evidence that shared Chromium or its profile has exited.
