package appsession

import (
	"time"

	"github.com/getan/golder/internal/agentcore"
)

// ItemKind classifies one transcript entry.
type ItemKind string

const (
	// ItemUser is a message the person sent.
	ItemUser ItemKind = "user"
	// ItemAssistant is model text: streaming while Done is false.
	ItemAssistant ItemKind = "assistant"
	// ItemThinking is reasoning text, shown collapsed by the view.
	ItemThinking ItemKind = "thinking"
	// ItemTool is one tool call and its (bounded) result.
	ItemTool ItemKind = "tool"
	// ItemNote is a permission-gate verdict (approval, denial, sandbox routing).
	ItemNote ItemKind = "note"
	// ItemNotice is a controller-level announcement (compaction, provider retry,
	// interrupt, persistence failure).
	ItemNotice ItemKind = "notice"
)

// Item is one row of the transcript. The view renders it and never mutates it;
// a change arrives as an Event that replaces the item of the same ID.
type Item struct {
	ID   string
	Kind ItemKind
	Text string
	// Done is true once the item can no longer change. Streaming assistant text
	// and a running tool are open.
	Done bool
	// Tool holds the structured call for ItemTool.
	Tool *ToolState
	// Note holds the verdict for ItemNote.
	Note *NoteState
	// Failed marks a tool error, a denied call or a failed run.
	Failed bool
	// Time is when the item first appeared.
	Time time.Time
}

// ToolState is one tool invocation, as the view shows it.
type ToolState struct {
	ID     string
	Name   string
	Input  map[string]any
	Output string
	// Details is structured tool metadata (for example bash_id). It is not
	// rendered wholesale; views pick known non-secret fields.
	Details map[string]any
	// Running is true until the executor reports the end.
	Running bool
	// Error is the executor's failure flag, not a parse result.
	Error bool
}

// TTYSummary is one interactive bash session available for terminal takeover.
type TTYSummary struct {
	ID       string
	Command  string
	Running  bool
	ExitCode int
}

// NoteState is one permission-gate announcement.
type NoteState struct {
	Tool      string
	Kind      int
	Level     string
	Summary   string
	Source    string
	Rationale string
	Mode      string
	// Granted is true for approvals that let a call proceed.
	Granted bool
	// Failure is true for denials and blocks.
	Failure bool
}

// ApprovalView is one pending human decision. The controller owns the reply
// channel; the view only renders Require and answers with an Answer.
type ApprovalView struct {
	ID string
	// Kind is "consent" or "trust".
	Kind string
	Tool string
	// Summary is the call's one-line preview.
	Summary string
	// Rationale is the reviewer's explanation, already in the user's language.
	Rationale string
	// Source names the deciding layer (jev, local, llm, static).
	Source string
	// Level is the gate's grade (allow, confirm, contain, deny).
	Level string
	// Mode is the approval mode in force.
	Mode string
	// Needs lists the capabilities the call requires.
	Needs []string
	// TrustDir names the folder a trust question is about.
	TrustDir string
	// Origin names a sub-agent that asked, when it is not this conversation.
	Origin string
}

// Answer is the view's decision for one approval.
type Answer int

const (
	// AnswerDeny blocks the call.
	AnswerDeny Answer = iota
	// AnswerOnce approves this call only.
	AnswerOnce
	// AnswerSession approves this shape of call for the rest of the session.
	AnswerSession
)

// Status is the live state the composer footer shows.
type Status struct {
	Model        string
	Provider     string
	Thinking     agentcore.ThinkingLevel
	Permission   string
	Running      bool
	RunID        string
	SessionID    string
	Title        string
	ContextUsed  int
	ContextLimit int
	ReadOnly     bool
	Writer       SessionWriterView
	// Error is the last controller error worth showing, "" when none.
	Error string
}

type SessionWriterView struct {
	Frontend string
	PID      int
	Since    time.Time
}

// Snapshot is a complete view of the controller, taken at one instant. A view
// builds from it and then applies Events.
type Snapshot struct {
	Status
	Project string
	// Items is the transcript in display order.
	Items []Item
	// Pending are the approvals waiting for an answer, oldest first.
	Pending []ApprovalView
}

// EventKind classifies a controller event.
type EventKind string

const (
	// EventItemAddressed means Item was appended.
	EventItemAdded EventKind = "item_added"
	// EventItemUpdated means the item of Item.ID changed.
	EventItemUpdated EventKind = "item_updated"
	// EventApproval means a new approval is pending.
	EventApproval EventKind = "approval_requested"
	// EventApprovalDone means the approval of ID was answered or withdrawn.
	EventApprovalDone EventKind = "approval_resolved"
	// EventStatus means the live configuration or run state changed.
	EventStatus EventKind = "status"
	// EventRunEnd means the run ended; Error carries a failure.
	EventRunEnd EventKind = "run_end"
	// EventComputerRequest asks the local view to offer an operator-owned picker.
	EventComputerRequest EventKind = "computer_request"
)

// Event is one ordered controller notification. Seq increases monotonically for
// the controller's lifetime, so a view can detect a missed event and re-read
// Snapshot.
type Event struct {
	Seq      uint64
	Session  string
	Kind     EventKind
	Item     *Item
	ID       string
	Approval *ApprovalView
	Status   *Status
	Error    string
}

// SessionSummary is one row of the session sidebar.
type SessionSummary struct {
	ID        string
	Title     string
	Model     string
	Provider  string
	Cwd       string
	UpdatedAt time.Time
	Current   bool
	Archived  bool
}

// ModelOption is one choice in the model picker.
type ModelOption struct {
	ID       string
	Provider string
	// Current marks the model in use.
	Current bool
	// Default marks the model a new session would start with.
	Default bool
}

// Action is one runnable command surfaced by the command palette: a slash
// command, a GUI action, or a skill.
type Action struct {
	ID           string
	Label        string
	Category     string
	Description  string
	ArgumentHint string
	Source       string
}

// ActionResult is what running an action produced.
type ActionResult struct {
	// Message is text to show in the transcript.
	Message string
	// Prompt, when non-empty, is a turn to run after Message.
	Prompt string
}

// FileStatus is one changed file in the project.
type FileStatus struct {
	Path    string
	Status  string
	Added   int
	Deleted int
}

// ChangeView selects which side of Git's index the review pane displays.
type ChangeView string

const (
	ChangesUnstaged ChangeView = "unstaged"
	ChangesStaged   ChangeView = "staged"
)

// Changes is the project's uncommitted work, for the changes drawer.
type Changes struct {
	Files      []FileStatus
	View       ChangeView
	Project    string
	RepoRoot   string
	Branch     string
	TotalFiles int
	Added      int
	Deleted    int
	Truncated  bool
	NotGitRepo bool
}

// FileDiff is one lazily loaded, size-bounded file patch.
type FileDiff struct {
	Path      string
	Patch     string
	Added     int
	Deleted   int
	Truncated bool
}
