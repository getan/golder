package larkmcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (p *Plugin) lark(ctx context.Context, d definition, in input, args []string) Result {
	o := in.Options
	if !d.Write {
		return p.run(ctx, "lark", args, false)
	}
	var readCommand []string
	var target map[string]any
	if strings.HasPrefix(d.Name, "sheet_") {
		target = subset(o, "url", "spreadsheet_token")
		preflight, _ := buildArgs([]string{"sheets", "+workbook-info"}, target)
		if before := p.run(ctx, "lark", preflight, false); !before.OK {
			return before
		}
		if d.Name == "sheet_write" && in.Operation == "table" {
			if err := validateTableWrite(o["sheets"]); err != nil {
				return failure("lark", "INVALID_TABLE", err.Error(), "Use typed sheets with mode=append/overwrite and explicit overwrite intent")
			}
		}
		if d.Name == "sheet_write" && in.Operation == "cells" {
			target = subset(o, "url", "spreadsheet_token", "sheet_id", "sheet_name", "range")
			readCommand = []string{"sheets", "+cells-get"}
		} else {
			readCommand = []string{"sheets", "+workbook-info"}
		}
	}
	if d.Name == "doc_write" && in.Operation != "create" {
		target = subset(o, "doc")
		before, _ := buildArgs([]string{"docs", "+fetch"}, target)
		if read := p.run(ctx, "lark", before, false); !read.OK {
			return read
		}
		readCommand = []string{"docs", "+fetch"}
	}
	if d.Name == "base_record_write" {
		target = subset(o, "base_token", "table_id")
		preflight, _ := buildArgs([]string{"base", "+field-list"}, target)
		schema := p.run(ctx, "lark", preflight, false)
		if !schema.OK {
			return schema
		}
		if in.Operation != "delete" {
			if err := validateBaseWrite(in.Operation, o["json"], schema.Data); err != nil {
				return failure("lark", "INVALID_RECORDS", err.Error(), "Resolve actual writable fields with base_schema; only inline CellValue data is accepted")
			}
		}
	}
	result := p.run(ctx, "lark", args, true)
	if !result.OK {
		return result
	}
	if d.Name == "doc_write" && in.Operation == "create" {
		id := findString(result.Data, "document_id")
		if id == "" {
			id = findString(result.Data, "doc_token")
		}
		if id != "" {
			readCommand = []string{"docs", "+fetch"}
			target = map[string]any{"doc": id}
		}
	}
	if len(readCommand) > 0 {
		readArgs, _ := buildArgs(readCommand, target)
		result = withReadback(result, p.run(ctx, "lark", readArgs, false))
	}
	if d.Name == "base_record_write" && in.Operation == "update" {
		payload, _ := o["json"].(map[string]any)
		records, _ := payload["update_records"].(map[string]any)
		back := map[string]any{}
		for id := range records {
			if len(back) >= 5 {
				break
			}
			readArgs, _ := buildArgs([]string{"base", "+record-get"}, map[string]any{"base_token": o["base_token"], "table_id": o["table_id"], "record_id": id})
			r := p.run(ctx, "lark", readArgs, false)
			if r.OK {
				back[id] = r.Data
			} else {
				back[id] = r.Error
				result.Partial = true
			}
		}
		result.Readback, result.Verification = back, "sampled_readback_only"
	}
	if findString(result.Data, "ignored_fields") != "" || containsKey(result.Data, "ignored_fields") {
		result.Partial = true
	}
	return result
}

func validateTableWrite(value any) error {
	root, ok := value.(map[string]any)
	if !ok {
		return errors.New("sheets must be an object with a sheets array")
	}
	sheets, ok := root["sheets"].([]any)
	if !ok || len(sheets) == 0 || len(sheets) > 20 {
		return errors.New("provide 1–20 typed sheet payloads")
	}
	rows := 0
	for _, value := range sheets {
		sheet, ok := value.(map[string]any)
		if !ok || sheet["name"] == nil {
			return errors.New("each sheet needs its exact name")
		}
		mode, _ := sheet["mode"].(string)
		if mode != "append" && mode != "overwrite" {
			return errors.New("each sheet needs an explicit mode: append or overwrite")
		}
		if mode == "overwrite" && sheet["allow_overwrite"] != true {
			return errors.New("overwrite needs explicit allow_overwrite=true")
		}
		data, ok := sheet["data"].([]any)
		if !ok {
			return errors.New("sheet.data must be a row array")
		}
		rows += len(data)
	}
	if rows > 2000 {
		return errors.New("write at most 2000 rows per call")
	}
	return nil
}

func validateBaseWrite(operation string, payload, schema any) error {
	root, ok := payload.(map[string]any)
	if !ok {
		return errors.New("json must be an inline record object")
	}
	var rows []any
	if operation == "create" {
		rows, _ = root["create_records"].([]any)
	} else {
		records, ok := root["update_records"].(map[string]any)
		if !ok {
			return errors.New("update_records must map exact record IDs to field maps")
		}
		for id, fields := range records {
			if id == "" {
				return errors.New("record ID is empty")
			}
			rows = append(rows, fields)
		}
	}
	if len(rows) == 0 || len(rows) > 200 {
		return errors.New("a batch must contain 1–200 records")
	}
	for _, value := range rows {
		fields, ok := value.(map[string]any)
		if !ok || len(fields) == 0 {
			return errors.New("each record must have a non-empty field map")
		}
		for key := range fields {
			field := findObject(schema, "field_name", key)
			if field == nil {
				field = findObject(schema, "field_id", key)
			}
			if field == nil {
				field = findObject(schema, "name", key)
			}
			if field == nil {
				return fmt.Errorf("field is not present in the current schema: %s", key)
			}
			kind := strings.ToLower(fmt.Sprint(field["type"]))
			if field["read_only"] == true || field["is_readonly"] == true || field["editable"] == false || kind == "20" || kind == "21" || kind == "17" || strings.Contains(kind, "formula") || strings.Contains(kind, "lookup") || strings.Contains(kind, "attachment") || strings.Contains(kind, "created") || strings.Contains(kind, "modified") {
				return fmt.Errorf("field is read-only or requires a separate attachment workflow: %s", key)
			}
		}
	}
	return nil
}

func containsKey(value any, key string) bool {
	switch v := value.(type) {
	case map[string]any:
		if _, ok := v[key]; ok {
			return true
		}
		for _, x := range v {
			if containsKey(x, key) {
				return true
			}
		}
	case []any:
		for _, x := range v {
			if containsKey(x, key) {
				return true
			}
		}
	}
	return false
}
