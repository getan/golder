// This file implements the apply_patch tool: one call carries a whole patch
// (add / update / move / delete, any number of files) in the codex apply_patch
// format. It replaces the former separate write and edit tools, so a
// multi-file change lands as one call and the model keeps one editing tool in
// context. The parser and matcher live in internal/patch; this file owns the
// workspace-path policy, the /rewind snapshots, and persistence.
package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/patch"
)

// ApplyPatchTool edits files under Root via an apply_patch-format patch.
type ApplyPatchTool struct {
	// Root bounds all writes; a path resolving outside Root is rejected. Empty
	// Root defaults to the current working directory.
	Root string
	// ExtraRoots are additional trusted directories a change may target even
	// though they lie outside Root (the skills directory, so the model can
	// author skills it advertises).
	ExtraRoots []string
	// Snap, when non-nil, records each file's prior content before it is
	// mutated so /rewind can roll the change back: every path a patch touches
	// is snapshotted, creates and deletes included.
	Snap *FileSnapshotRecorder
}

// applyPatchArgs is the decoded argument shape. The patched text is the tool
// input, exactly as in codex; it travels as one JSON string only because
// pigo's tool interface is JSON-typed, which is why the model escapes
// newlines as \n.
type applyPatchArgs struct {
	Patch string `json:"patch"`
}

// Name implements AgentTool.
func (t *ApplyPatchTool) Name() string { return "apply_patch" }

// Description implements AgentTool.
func (t *ApplyPatchTool) Description() string {
	return "Edit files with one patch: add, update, move, or delete any number " +
		"of files per call. The patch text uses the *** Begin Patch / *** End " +
		"Patch format; update chunks are line-oriented diffs with optional " +
		"@@ context and fuzzy matching. Use this instead of shelling out to " +
		"patch utilities."
}

// Schema implements AgentTool.
func (t *ApplyPatchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "patch": {"type": "string", "description": "The complete patch text, from \"*** Begin Patch\" through \"*** End Patch\"."}
  },
  "required": ["patch"],
  "additionalProperties": false
}`)
}

// ExecutionMode implements AgentTool. Patches mutate the filesystem →
// sequential so a batch cannot race concurrent writes to the same tree.
func (t *ApplyPatchTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

// resolvePath resolves p against Root (or any ExtraRoots) via the shared
// resolveWithin boundary policy — the same guard every file tool uses.
func (t *ApplyPatchTool) resolvePath(p string) (string, error) {
	if len(t.ExtraRoots) == 0 {
		return resolveWithin(t.Root, p)
	}
	return resolveWithinAny(append([]string{t.Root}, t.ExtraRoots...), p)
}

// Execute implements AgentTool. Every failure — malformed patch, path escape,
// missing file, stale context — is decided before the first byte is written,
// so a rejected patch leaves the tree untouched. The returned Go error is
// reserved for nothing here; failures are error results.
func (t *ApplyPatchTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[applyPatchArgs](args, "apply_patch")
	if bad != nil {
		return *bad, nil
	}
	if strings.TrimSpace(a.Patch) == "" {
		return errorResult("apply_patch: patch is required"), nil
	}
	ops, err := patch.Parse(a.Patch)
	if err != nil {
		return errorResult("apply_patch: " + err.Error()), nil
	}
	changes, err := patch.Compute(ops, t.resolvePath)
	if err != nil {
		return errorResult("apply_patch: " + err.Error()), nil
	}

	var summaries []string
	var diffs strings.Builder
	touched := make([]string, 0, len(changes))
	for _, ch := range changes {
		label := t.displayPath(ch.Path)
		if err := t.persist(ch); err != nil {
			return errorResult(fmt.Sprintf("apply_patch: %v", err)), nil
		}
		touched = append(touched, label)
		if ch.Kind == patch.ChangeMove {
			summaries = append(summaries, summarizeChange(ch, t.displayPath(ch.OldPath)+" \u2192 "+label))
		} else {
			summaries = append(summaries, summarizeChange(ch, label))
		}
		if diff := unifiedDiff(label, ch.OldContent, ch.NewContent); diff != "" {
			diffs.WriteString(diff)
		}
	}

	msg := fmt.Sprintf("Applied %d change(s):\n%s", len(changes), strings.Join(summaries, "\n"))
	if diff := diffs.String(); diff != "" {
		msg += "\n" + diff
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(msg)},
		Details: map[string]any{"changes": len(changes), "files": touched, "diff": diffs.String()},
	}, nil
}

// persist writes one computed change, snapshotting each affected path first so
// /rewind can restore the turn.
func (t *ApplyPatchTool) persist(ch patch.Change) error {
	switch ch.Kind {
	case patch.ChangeAdd, patch.ChangeUpdate, patch.ChangeMove:
		if ch.Kind == patch.ChangeMove {
			t.Snap.Record(ch.OldPath)
		}
		t.Snap.Record(ch.Path)
		if dir := filepath.Dir(ch.Path); dir != "" {
			if err := os.MkdirAll(dir, dirPerm); err != nil {
				return fmt.Errorf("cannot create parent directories for %s: %v", ch.Path, err)
			}
		}
		if err := os.WriteFile(ch.Path, []byte(ch.NewContent), filePerm); err != nil {
			return fmt.Errorf("cannot write %s: %v", ch.Path, err)
		}
		if ch.Kind == patch.ChangeMove {
			if err := os.Remove(ch.OldPath); err != nil {
				return fmt.Errorf("moved to %s but cannot remove %s: %v", ch.Path, ch.OldPath, err)
			}
		}
	case patch.ChangeDelete:
		t.Snap.Record(ch.Path)
		if err := os.Remove(ch.Path); err != nil {
			return fmt.Errorf("cannot delete %s: %v", ch.Path, err)
		}
	default:
		return fmt.Errorf("unknown change kind %d", ch.Kind)
	}
	return nil
}

// displayPath renders one path for the summary and diff labels,
// relative to Root when possible so the model sees workspace-relative names.
func (t *ApplyPatchTool) displayPath(path string) string {
	base := t.Root
	if base == "" {
		base, _ = os.Getwd()
	}
	rel, err := filepath.Rel(base, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return filepath.ToSlash(rel)
}

// summarizeChange renders one result line: a one-letter verb plus the path,
// with a move showing both ends.
func summarizeChange(ch patch.Change, label string) string {
	switch ch.Kind {
	case patch.ChangeAdd:
		return "  A " + label
	case patch.ChangeUpdate:
		return "  U " + label
	case patch.ChangeDelete:
		return "  D " + label
	case patch.ChangeMove:
		return "  M " + label
	default:
		return "  ? " + label
	}
}
