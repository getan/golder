package larkmcp

import (
	"encoding/json"
	"sort"
	"strings"
)

type operation struct {
	Command  []string
	Required []string
}

type definition struct {
	Name, Description, Service string
	Write                      bool
	Operations                 map[string]operation
	Default                    string
	Options                    map[string]any
}

func text(description string) map[string]any {
	return map[string]any{"type": "string", "maxLength": 16000, "description": description}
}
func object(description string) map[string]any {
	return map[string]any{"type": "object", "description": description}
}
func array(description string) map[string]any {
	return map[string]any{"type": "array", "maxItems": 200, "items": map[string]any{}, "description": description}
}
func integer(max int) map[string]any {
	return map[string]any{"type": "integer", "minimum": 1, "maximum": max}
}
func boolean() map[string]any { return map[string]any{"type": "boolean"} }

func options(names string) map[string]any {
	props := map[string]any{}
	for _, name := range strings.Fields(names) {
		props[name] = text("CLI " + strings.ReplaceAll(name, "_", "-"))
	}
	return props
}

func op(command string, required ...string) operation {
	return operation{Command: strings.Fields(command), Required: required}
}
func single(name, description, command, flags string, write bool, required ...string) definition {
	return definition{Name: name, Description: description, Service: "lark", Write: write, Default: "run", Operations: map[string]operation{"run": op(command, required...)}, Options: options(flags)}
}

func definitions() []definition {
	defs := []definition{
		single("doc_search", "Search Feishu documents/Wiki by topic and filters. Returns a bounded page, not a full-corpus scan.", "drive +search", "query doc_types created_since created_until page_token page_size mine", false),
		single("doc_read", "Read a Feishu document, outline, section or keyword excerpt. Use scope to avoid fetching a whole large document.", "docs +fetch", "doc scope keyword start_block_id end_block_id detail doc_format", false, "doc"),
		{Name: "doc_write", Description: "Create, append or replace a specific text/block in a document as the user. Requires confirmed intent; no default full overwrite.", Service: "lark", Write: true, Default: "append", Options: options("doc title content pattern block_id parent_token"), Operations: map[string]operation{
			"create": op("docs +create", "title", "content"), "append": op("docs +update", "doc", "content"), "replace_text": op("docs +update", "doc", "pattern", "content"), "replace_block": op("docs +update", "doc", "block_id", "content"),
		}},
		single("chat_search", "Find Feishu group chats by name/keyword. Resolve an unambiguous chat before sending.", "im +chat-search", "query page_size page_token", false),
		single("message_search", "Search Feishu messages by keyword, chats, sender and time.", "im +messages-search", "query chat_id sender start end page_size page_token", false),
		single("chat_history", "Read a bounded page of messages from a specific Feishu group or user chat.", "im +chat-messages-list", "chat_id user_id start end page_size page_token", false),
		single("message_send", "Send text/Markdown to an exact Feishu chat or user as the logged-in user. Obtain operator approval for recipient and full body.", "im +messages-send", "chat_id user_id text markdown", true),
		{Name: "project_schema", Description: "Discover Feishu Project spaces, work-item types/fields/roles, users and allowed workflow transitions. Project is not Approval.", Service: "project", Default: "projects", Options: options("query project_key work_item_type work_item_id field_query field_keys role_query page_num transition_id"), Operations: map[string]operation{
			"projects": op("project search"), "types": op("workitem meta-types", "project_key"), "fields": op("workitem meta-fields", "project_key", "work_item_type"), "roles": op("workitem meta-roles", "project_key", "work_item_type"), "users": op("user search", "project_key", "query"), "me": op("user me"), "transitions": op("workflow list-state-transitions", "project_key", "work_item_id"), "required": op("workflow list-state-required", "project_key", "work_item_id", "transition_id"), "nodes": op("workflow get-node", "project_key", "work_item_id"), "decode_url": op("url decode", "url"),
		}},
		{Name: "ticket_search", Description: "Search Feishu Project requirements/bugs/tasks in one project/type using a bounded, escaped read-only query. Discover real type/status keys first.", Service: "project", Default: "search", Options: options("project_key work_item_type query assignee status session_id group_pagination_list limit"), Operations: map[string]operation{"search": op("workitem query", "project_key")}},
		{Name: "ticket_get", Description: "Read a Feishu Project work item by actual ID and project key. Returns its real work-item type; do not assume story/issue.", Service: "project", Default: "run", Options: options("project_key work_item_id fields"), Operations: map[string]operation{"run": op("workitem get", "project_key", "work_item_id")}},
		{Name: "ticket_update", Description: "Update specific Feishu Project fields (title, assignee, priority, dates). Reads current item/schema, stringifies field_value, then reads back; no status-field shortcut.", Service: "project", Write: true, Default: "run", Options: options("project_key work_item_id fields expected"), Operations: map[string]operation{"run": op("workitem update", "project_key", "work_item_id", "fields")}},
		{Name: "ticket_transition", Description: "Perform one allowed Feishu Project state transition or confirm one workflow node. Not Approval approve/reject. Requires exact operator approval.", Service: "project", Write: true, Default: "state", Options: options("project_key work_item_id transition_id node_id fields"), Operations: map[string]operation{"state": op("workflow transition-state", "project_key", "work_item_id", "transition_id"), "node": op("workflow transition", "project_key", "work_item_id", "node_id")}},
		{Name: "sheet_read", Description: "Inspect workbook/sheet metadata, values, formulas and styles. Pick a real sheet and bounded A1 range first; raw cells preserve long identifiers.", Service: "lark", Default: "workbook", Options: options("url spreadsheet_token sheet_id sheet_name range max_chars include"), Operations: map[string]operation{"workbook": op("sheets +workbook-info"), "sheet": op("sheets +sheet-info"), "values": op("sheets +csv-get", "range"), "cells": op("sheets +cells-get", "range")}},
		{Name: "sheet_write", Description: "Write a bounded cell range/formulas or typed table/append without touching other regions. Reads metadata first; reports readback, never claims sampled/full verification interchangeably.", Service: "lark", Write: true, Default: "cells", Options: options("url spreadsheet_token sheet_id sheet_name range cells sheets styles"), Operations: map[string]operation{"cells": op("sheets +cells-set", "range", "cells"), "table": op("sheets +table-put", "sheets")}},
		{Name: "sheet_structure", Description: "Create a sub-sheet or insert rows/columns. Explicit operation and target required; no delete/clear/rollback.", Service: "lark", Write: true, Default: "create", Options: options("url spreadsheet_token sheet_id sheet_name title index position count"), Operations: map[string]operation{"create": op("sheets +sheet-create", "title"), "insert": op("sheets +dim-insert", "position", "count")}},
		{Name: "base_resolve", Description: "Resolve a Feishu Base URL or search its title; disambiguate the result before querying or writing records.", Service: "lark", Default: "url", Options: options("url title"), Operations: map[string]operation{"url": op("base +url-resolve", "url"), "title": op("base +title-resolve", "title")}},
		{Name: "base_schema", Description: "List Base tables or real field types/options before working with records.", Service: "lark", Default: "tables", Options: options("base_token table_id page_size page_token"), Operations: map[string]operation{"tables": op("base +table-list", "base_token"), "fields": op("base +field-list", "base_token", "table_id")}},
		{Name: "base_record_search", Description: "Read/search a bounded page of Base records by IDs or structured filter; preserve pagination and ignored fields.", Service: "lark", Default: "search", Options: options("base_token table_id record_id json page_size page_token"), Operations: map[string]operation{"search": op("base +record-search", "base_token", "table_id", "json"), "list": op("base +record-list", "base_token", "table_id"), "get": op("base +record-get", "base_token", "table_id", "record_id")}},
		{Name: "base_record_write", Description: "Create/update up to 200 Base records or delete one exact record ID. Reads real field schema, rejects read-only fields, and reports partial/unknown results without replay.", Service: "lark", Write: true, Default: "update", Options: options("base_token table_id record_id json"), Operations: map[string]operation{"create": op("base +record-batch-create", "base_token", "table_id", "json"), "update": op("base +record-batch-update", "base_token", "table_id", "json"), "delete": op("base +record-delete", "base_token", "table_id", "record_id")}},
	}
	for i := range defs {
		d := &defs[i]
		for _, key := range []string{"mine"} {
			if _, ok := d.Options[key]; ok {
				d.Options[key] = boolean()
			}
		}
		for _, key := range []string{"page_size", "page_num", "limit", "max_chars", "index", "count"} {
			if _, ok := d.Options[key]; ok {
				d.Options[key] = integer(200)
			}
		}
		if _, ok := d.Options["max_chars"]; ok {
			d.Options["max_chars"] = integer(40000)
		}
		if d.Name == "doc_search" {
			d.Options["page_size"] = integer(20)
		}
		if d.Name == "ticket_search" {
			d.Options["limit"] = integer(50)
			d.Options["group_pagination_list"] = array("Continuation group_id/page_num from a previous query")
		}
		if d.Name == "project_schema" {
			d.Options["url"] = text("Feishu Project URL for decode_url")
			d.Options["field_keys"] = array("Exact field keys")
			d.Options["user_key"] = text("Project user key from user me, required for transitions")
			d.Options["state_key"] = text("Target state/node key for required-fields lookup")
			d.Operations["transitions"] = op("workflow list-state-transitions", "project_key", "work_item_id", "work_item_type", "user_key")
			d.Operations["required"] = op("workflow list-state-required", "project_key", "work_item_id", "state_key")
		}
		if d.Name == "ticket_get" {
			d.Options["fields"] = array("Explicit field keys; omit for the default overview")
		}
		if d.Name == "ticket_update" {
			d.Options["fields"] = array("Objects {field_key,field_value}; arrays/objects are serialized to the CLI string protocol")
			d.Options["expected"] = object("Optional previously observed field-key/value map; refuse if changed")
		}
		if d.Name == "ticket_transition" {
			d.Options["state_key"] = text("Target state key from the selected transition")
			d.Operations["state"] = op("workflow transition-state", "project_key", "work_item_id", "transition_id", "state_key")
			delete(d.Options, "fields") // required fields must be updated/reconfirmed separately
		}
		for _, key := range []string{"json", "cells", "sheets", "styles"} {
			if _, ok := d.Options[key]; ok {
				d.Options[key] = object("Inline typed JSON payload; no @file or stdin references")
			}
		}
		if d.Name == "sheet_write" {
			d.Options["cells"] = array("2D array of typed cell objects: [[{value:...,formula:...}]]")
		}
		if d.Name == "base_schema" {
			delete(d.Options, "page_token")
			d.Options["offset"] = integer(100000)
		}
		if d.Name == "base_record_search" {
			delete(d.Options, "page_token")
			d.Options["offset"] = integer(100000)
			d.Options["limit"] = integer(200)
		}
	}
	return defs
}

func (d definition) schema() json.RawMessage {
	names := make([]string, 0, len(d.Operations))
	for name := range d.Operations {
		names = append(names, name)
	}
	sort.Strings(names)
	props := map[string]any{
		"operation": map[string]any{"type": "string", "enum": names, "default": d.Default},
		"options":   map[string]any{"type": "object", "additionalProperties": false, "properties": d.Options},
	}
	required := []string{"options"}
	if d.Write {
		props["confirm"] = map[string]any{"type": "boolean", "const": true, "description": "Execution intent only; the host must separately obtain exact human approval"}
		required = append(required, "confirm")
	}
	b, _ := json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": required})
	return b
}
