package mygoui

import (
	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/explore"
)

// transcriptRow projects a tool and its review details into one virtual list
// row. Indices refer to the live slices, so streaming updates need no rebuild.
type transcriptRow struct {
	key           string
	itemIndex     int
	approvalIndex int
	placeholder   *appsession.Item
	notes         []int
	approvals     []int
	members       []transcriptRow // consecutive exploration tool rows, if grouped
}

// toolPresentationKey is only a display association, never an authorization
// binding. Child-agent requests and calls without exact ids remain standalone.
func toolPresentationKey(runID, callID, origin string) string {
	if runID == "" || callID == "" || origin != "" {
		return ""
	}
	return runID + "-tool-" + callID
}

func (a *App) projectedTranscriptRows() []transcriptRow {
	if a.transcriptRows != nil && !a.transcriptRowsDirty && a.transcriptRowsItems == len(a.items) && a.transcriptRowsPending == len(a.pending) {
		return a.transcriptRows
	}
	rows := make([]transcriptRow, 0, len(a.items)+len(a.pending))
	tools := make(map[string]int)
	type identity struct {
		callID   string
		name     string
		conflict bool
	}
	known := make(map[string]identity)
	remember := func(key, callID, name string) {
		prior, exists := known[key]
		if !exists {
			known[key] = identity{callID: callID, name: name}
			return
		}
		if prior.callID != callID || prior.name != "" && name != "" && prior.name != name {
			prior.conflict = true
		}
		if prior.name == "" {
			prior.name = name
		}
		known[key] = prior
	}
	matches := func(key, callID, name string) bool {
		entry, ok := known[key]
		return ok && !entry.conflict && entry.callID == callID && (entry.name == "" || name == "" || entry.name == name)
	}
	for _, item := range a.items {
		if item.Kind == appsession.ItemTool && item.Tool != nil {
			remember(item.ID, item.Tool.ID, item.Tool.Name)
		}
	}
	for _, approval := range a.pending {
		if key := toolPresentationKey(approval.RunID, approval.ToolCallID, approval.Origin); key != "" {
			remember(key, approval.ToolCallID, approval.Tool)
		}
	}
	ensureTool := func(key, name, callID string) int {
		if index, ok := tools[key]; ok {
			return index
		}
		placeholder := &appsession.Item{ID: key, Kind: appsession.ItemTool, Text: name,
			Tool: &appsession.ToolState{ID: callID, Name: name, Running: true, AwaitingStart: true}}
		index := len(rows)
		rows = append(rows, transcriptRow{key: key, itemIndex: -1, approvalIndex: -1, placeholder: placeholder})
		tools[key] = index
		return index
	}
	for i, item := range a.items {
		if item.Kind == appsession.ItemNote && item.Note != nil {
			note := item.Note
			if key := toolPresentationKey(note.RunID, note.ToolCallID, note.Origin); key != "" && matches(key, note.ToolCallID, note.Tool) {
				index := ensureTool(key, note.Tool, note.ToolCallID)
				rows[index].notes = append(rows[index].notes, i)
				continue
			}
		}
		if item.Kind == appsession.ItemTool && item.Tool != nil {
			index := ensureTool(item.ID, item.Tool.Name, item.Tool.ID)
			rows[index].itemIndex, rows[index].placeholder = i, nil
			continue
		}
		rows = append(rows, transcriptRow{key: item.ID, itemIndex: i, approvalIndex: -1})
	}
	for i, approval := range a.pending {
		if key := toolPresentationKey(approval.RunID, approval.ToolCallID, approval.Origin); key != "" && matches(key, approval.ToolCallID, approval.Tool) {
			index := ensureTool(key, approval.Tool, approval.ToolCallID)
			rows[index].approvals = append(rows[index].approvals, i)
			continue
		}
		rows = append(rows, transcriptRow{key: approval.ID, itemIndex: -1, approvalIndex: i})
	}
	a.transcriptRows = a.groupExploreRows(rows)
	a.transcriptRowsItems, a.transcriptRowsPending = len(a.items), len(a.pending)
	a.transcriptRowsDirty = false
	return a.transcriptRows
}

// groupExploreRows compacts consecutive native read-only tools after reviews
// have been associated with their exact calls. Other transcript rows remain
// boundaries, just as they do in the TUI.
func (a *App) groupExploreRows(rows []transcriptRow) []transcriptRow {
	grouped := make([]transcriptRow, 0, len(rows))
	for _, row := range rows {
		item, ok := a.transcriptRowItem(row)
		if ok && item.Kind == appsession.ItemTool && item.Tool != nil && explore.IsTool(item.Tool.Name) {
			if n := len(grouped); n > 0 && len(grouped[n-1].members) > 0 {
				grouped[n-1].members = append(grouped[n-1].members, row)
			} else {
				// Keep the first call's list identity when a run grows, so the
				// virtual list retains its scroll anchor during streaming.
				grouped = append(grouped, transcriptRow{key: row.key, itemIndex: -1, approvalIndex: -1, members: []transcriptRow{row}})
			}
			continue
		}
		grouped = append(grouped, row)
	}
	return grouped
}

func (a *App) transcriptRowItem(row transcriptRow) (appsession.Item, bool) {
	if row.itemIndex >= 0 && row.itemIndex < len(a.items) {
		return a.items[row.itemIndex], true
	}
	if row.placeholder != nil {
		return *row.placeholder, true
	}
	return appsession.Item{}, false
}

func approvalRowIndex(rows []transcriptRow, pending []appsession.ApprovalView, id string) int {
	for i, row := range rows {
		if len(row.members) > 0 && approvalRowIndex(row.members, pending, id) >= 0 {
			return i
		}
		if row.approvalIndex >= 0 && row.approvalIndex < len(pending) && pending[row.approvalIndex].ID == id {
			return i
		}
		for _, index := range row.approvals {
			if index >= 0 && index < len(pending) && pending[index].ID == id {
				return i
			}
		}
	}
	return -1
}

func (a *App) transcriptRowView(c *ui.Context, row transcriptRow) {
	if len(row.members) > 0 {
		a.exploreGroupView(c, row)
		return
	}
	if row.approvalIndex >= 0 {
		a.approvalCard(c, a.pending[row.approvalIndex])
		return
	}
	item, ok := a.transcriptRowItem(row)
	if !ok {
		return
	}
	if item.Kind != appsession.ItemTool {
		a.itemView(c, item)
		return
	}
	a.toolCardDetails(c, item, row.notes, row.approvals)
}
