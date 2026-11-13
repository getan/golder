package larkmcp

import (
	"fmt"
	"strings"
)

// The aggregate tool schema contains options for multiple commands. Check the
// options of the expanded operations against the pinned CLI contract before
// dispatch, rather than silently passing flags to a different operation.
func validateSheetOptions(name string, in input) error {
	var flags string
	switch name + ":" + in.Operation {
	case "sheet_read:search":
		flags = "sheet_id sheet_name range find match_case match_entire_cell regex include_formulas"
	case "sheet_write:replace":
		flags = "sheet_id sheet_name range find replacement match_case match_entire_cell regex include_formulas dry_run"
	case "sheet_write:clear":
		flags = "sheet_id sheet_name range scope dry_run"
	case "sheet_structure:copy":
		flags = "sheet_id sheet_name title index dry_run"
	case "sheet_structure:rename":
		flags = "sheet_id sheet_name title dry_run"
	case "sheet_structure:move":
		flags = "sheet_id sheet_name index dry_run"
	case "sheet_structure:delete":
		flags = "sheet_id dry_run"
	case "sheet_structure:hide", "sheet_structure:unhide":
		flags = "sheet_id sheet_name dry_run"
	default:
		return nil
	}
	allowed := map[string]bool{"url": true, "spreadsheet_token": true}
	for _, flag := range strings.Fields(flags) {
		allowed[flag] = true
	}
	for key := range in.Options {
		if !allowed[key] {
			return fmt.Errorf("%s does not support option %q", in.Operation, key)
		}
	}
	if in.Operation == "clear" {
		if scope, ok := in.Options["scope"].(string); ok && scope != "content" && scope != "formats" && scope != "all" {
			return fmt.Errorf("clear scope must be content, formats or all")
		}
	}
	return nil
}

// Only a well-formed, complete workbook sheets array is evidence of absence.
// Unknown output shapes are not allowed to authorize or verify deletion.
func workbookContainsSheet(value any, id string) (bool, bool) {
	root, ok := value.(map[string]any)
	if !ok {
		return false, false
	}
	items, ok := root["sheets"].([]any)
	if !ok || len(items) == 0 {
		return false, false
	}
	found := false
	for _, item := range items {
		sheet, ok := item.(map[string]any)
		if !ok {
			return false, false
		}
		sheetID, ok := sheet["sheet_id"].(string)
		if !ok || strings.TrimSpace(sheetID) == "" {
			return false, false
		}
		if sheetID == id {
			if found {
				return false, false
			}
			found = true
		}
	}
	return found, true
}
