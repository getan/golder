package larkmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var identifier = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

var errSearchAssignee = errors.New("assignee only accepts the literal 'me'; user IDs, user keys, open IDs, emails and display names are not supported")

func projectArgs(command []string, params map[string]any) []string {
	b, _ := json.Marshal(params)
	return append(append([]string{}, command...), "--params="+string(b))
}

func (p *Plugin) project(ctx context.Context, d definition, in input, operation operation) Result {
	o := in.Options
	if d.Name == "ticket_search" {
		params, err := searchParams(o)
		if err != nil {
			if errors.Is(err, errSearchAssignee) {
				return failure("project", "INVALID_QUERY", err.Error(), "Set assignee to 'me' for the current status operator, or omit assignee to search without that filter; other-user and watcher filters are not supported by ticket_search")
			}
			return failure("project", "INVALID_QUERY", err.Error(), "Discover a real project/type and use structured filters")
		}
		return p.run(ctx, "project", projectArgs(operation.Command, params), false)
	}
	if d.Name == "project_schema" && in.Operation == "decode_url" {
		rawURL, _ := o["url"].(string)
		result := p.run(ctx, "project", []string{"url", "decode", "--url=" + rawURL}, false)
		if !result.OK && result.Error != nil {
			// A missing/invalid CLI result is not evidence that the URL is bad,
			// nor that the user lacks a project role. Keep the failure specific
			// and leave the original link for the operator to retry.
			result.Error.Code = "PROJECT_URL_DECODE_FAILED"
			result.Error.Message = "Project URL could not be decoded; check the URL and the installed Project CLI"
			result.Error.NextAction = "Use a full Feishu Project work-item URL; if it is valid, verify Project login and CLI availability"
		}
		return result
	}
	if d.Name == "project_schema" && (in.Operation == "fields" || in.Operation == "roles") && o["page_num"] == nil {
		o["page_num"] = 1
	}
	if !d.Write {
		return p.run(ctx, "project", projectArgs(operation.Command, o), false)
	}
	target := subset(o, "project_key", "work_item_id")
	before := p.run(ctx, "project", projectArgs([]string{"workitem", "get"}, target), false)
	if !before.OK {
		return before
	}
	params := subset(o, "project_key", "work_item_id", "transition_id", "node_id")
	if d.Name == "ticket_update" {
		fields, ok := o["fields"].([]any)
		if !ok || len(fields) == 0 || len(fields) > 50 {
			return failure("project", "INVALID_FIELDS", "Provide 1–50 field objects", "Use real keys from project_schema")
		}
		kind := findString(before.Data, "work_item_type", "key")
		if kind == "" {
			kind = findString(before.Data, "work_item_type_key")
		}
		if kind == "" {
			return failure("project", "TYPE_REQUIRED", "Could not determine the actual work-item type", "Read ticket_get; do not assume story/issue")
		}
		keys := make([]string, 0, len(fields))
		normalized := make([]map[string]any, 0, len(fields))
		for _, value := range fields {
			field, ok := value.(map[string]any)
			if !ok {
				return failure("project", "INVALID_FIELDS", "Each field must have field_key and field_value", "Read project_schema")
			}
			key, _ := field["field_key"].(string)
			if key == "" || key == "status" || key == "work_item_status" || key == "template" {
				return failure("project", "FIELD_BLOCKED", "Status/template changes are not ordinary field updates", "Use ticket_transition for status")
			}
			v, present := field["field_value"]
			if !present {
				return failure("project", "INVALID_FIELDS", "field_value is missing", "Use the actual field protocol")
			}
			if _, ok := v.(string); !ok {
				b, _ := json.Marshal(v)
				v = string(b)
			}
			keys = append(keys, key)
			normalized = append(normalized, map[string]any{"field_key": key, "field_value": v})
		}
		metadata := p.run(ctx, "project", projectArgs([]string{"workitem", "meta-fields"}, map[string]any{"project_key": o["project_key"], "work_item_type": kind, "field_keys": keys, "page_num": 1}), false)
		if !metadata.OK {
			return metadata
		}
		for _, key := range keys {
			field := findObject(metadata.Data, "field_key", key)
			if field == nil {
				field = findObject(metadata.Data, "key", key)
			}
			if field == nil || field["read_only"] == true || field["is_readonly"] == true || field["editable"] == false {
				return failure("project", "FIELD_NOT_WRITABLE", "Field is absent or not writable: "+key, "Check project role and field permissions")
			}
		}
		if expected, ok := o["expected"].(map[string]any); ok {
			current := p.run(ctx, "project", projectArgs([]string{"workitem", "get"}, map[string]any{"project_key": o["project_key"], "work_item_id": o["work_item_id"], "fields": keys}), false)
			if !current.OK {
				return current
			}
			for key, value := range expected {
				field := projectField(current.Data, key)
				if field == nil || !projectFieldValueEqual(field["value"], value) {
					return failure("project", "CONFLICT", "The previously observed field changed or could not be checked: "+key, "Read it again and reconfirm the update")
				}
			}
		}
		params["fields"] = normalized
		target["fields"] = keys
	} else if in.Operation == "state" {
		me := p.run(ctx, "project", projectArgs([]string{"user", "me"}, map[string]any{}), false)
		if !me.OK {
			return me
		}
		user := findString(me.Data, "user_key")
		kind := findString(before.Data, "work_item_type", "key")
		if user == "" || kind == "" {
			return failure("project", "CONTEXT_REQUIRED", "Cannot resolve actual user/type for the transition", "Inspect user me and ticket_get")
		}
		transitionParams := subset(target, "project_key", "work_item_id")
		transitionParams["user_key"], transitionParams["work_item_type"] = user, kind
		transitions := p.run(ctx, "project", projectArgs([]string{"workflow", "list-state-transitions"}, transitionParams), false)
		if !transitions.OK {
			return transitions
		}
		id, _ := o["transition_id"].(string)
		if findObject(transitions.Data, "transition_id", id) == nil && findObject(transitions.Data, "id", id) == nil {
			return failure("project", "TRANSITION_UNAVAILABLE", "This transition is no longer available", "Refresh project_schema transitions and reconfirm")
		}
		requiredParams := subset(target, "project_key", "work_item_id")
		requiredParams["state_key"], requiredParams["mode"] = o["state_key"], "unfinished"
		required := p.run(ctx, "project", projectArgs([]string{"workflow", "list-state-required"}, requiredParams), false)
		if !required.OK {
			return required
		}
	} else {
		params["action"] = "confirm"
	}
	result := p.run(ctx, "project", projectArgs(operation.Command, params), true)
	if !result.OK {
		return result
	}
	readback := p.run(ctx, "project", projectArgs([]string{"workitem", "get"}, target), false)
	result = withReadback(result, readback)
	return result
}

func searchParams(o map[string]any) (map[string]any, error) {
	project, _ := o["project_key"].(string)
	if !identifier.MatchString(project) {
		return nil, errors.New("project_key must be an actual resolved identifier")
	}
	if value, present := o["assignee"]; present {
		assignee, ok := value.(string)
		if !ok || assignee != "me" {
			return nil, errSearchAssignee
		}
	}
	if session, _ := o["session_id"].(string); session != "" {
		if o["group_pagination_list"] == nil {
			return nil, errors.New("continuation needs group_pagination_list")
		}
		return subset(o, "project_key", "session_id", "group_pagination_list"), nil
	}
	kind, _ := o["work_item_type"].(string)
	if !identifier.MatchString(kind) {
		return nil, errors.New("discover and provide the actual work_item_type key")
	}
	limit := 20
	if n, ok := o["limit"].(float64); ok {
		if n < 1 || n > 50 || n != float64(int(n)) {
			return nil, errors.New("limit must be 1–50")
		}
		limit = int(n)
	}
	query := fmt.Sprintf("SELECT `work_item_id`, `name`, `work_item_status` FROM `%s`.`%s`", project, kind)
	var conditions []string
	if keyword, _ := o["query"].(string); keyword != "" {
		if strings.ContainsRune(keyword, 0) || strings.Contains(keyword, "\\") {
			return nil, errors.New("query contains unsupported control/escape characters")
		}
		keyword = strings.NewReplacer("'", "''", "%", "\\%", "_", "\\_").Replace(keyword)
		conditions = append(conditions, "`name` LIKE '%"+keyword+"%'")
	}
	if status, _ := o["status"].(string); status != "" {
		conditions = append(conditions, "`work_item_status` = '"+strings.ReplaceAll(status, "'", "''")+"'")
	}
	if _, present := o["assignee"]; present {
		conditions = append(conditions, "array_contains(`current_status_operator`, current_login_user())")
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += fmt.Sprintf(" LIMIT %d", limit)
	return map[string]any{"project_key": project, "mql": query}, nil
}

func subset(input map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, key := range keys {
		if value, ok := input[key]; ok {
			out[key] = value
		}
	}
	return out
}

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// projectField returns one entry from the actual work_item_fields response. The
// Project CLI uses {"key":"priority","value":{...}}, while some older fixtures
// used field_key; support both without guessing from an arbitrary nested map.
func projectField(value any, key string) map[string]any {
	if root, ok := value.(map[string]any); ok {
		if fields, ok := root["work_item_fields"]; ok {
			if field := findFieldEntry(fields, key); field != nil {
				return field
			}
		}
	}
	return findFieldEntry(value, key)
}

func findFieldEntry(value any, key string) map[string]any {
	switch v := value.(type) {
	case map[string]any:
		if fmt.Sprint(v["key"]) == key || fmt.Sprint(v["field_key"]) == key {
			return v
		}
		for _, child := range v {
			if field := findFieldEntry(child, key); field != nil {
				return field
			}
		}
	case []any:
		for _, child := range v {
			if field := findFieldEntry(child, key); field != nil {
				return field
			}
		}
	}
	return nil
}

// projectFieldValueEqual compares the value shape returned by Project with the
// value the operator copied into expected. Option/person objects expose the
// meaningful scalar under value/label/key/name/id; arrays compare element-wise.
func projectFieldValueEqual(actual, expected any) bool {
	if sameJSON(actual, expected) {
		return true
	}
	switch value := actual.(type) {
	case map[string]any:
		for _, key := range []string{"value", "label", "key", "name", "id", "text"} {
			if candidate, ok := value[key]; ok && sameJSON(candidate, expected) {
				return true
			}
		}
	case []any:
		if wanted, ok := expected.([]any); ok {
			if len(value) != len(wanted) {
				return false
			}
			for i := range value {
				if !projectFieldValueEqual(value[i], wanted[i]) {
					return false
				}
			}
			return true
		}
		for _, item := range value {
			if projectFieldValueEqual(item, expected) {
				return true
			}
		}
	}
	return false
}

func findObject(value any, key, want string) map[string]any {
	switch v := value.(type) {
	case map[string]any:
		if fmt.Sprint(v[key]) == want {
			return v
		}
		for _, x := range v {
			if found := findObject(x, key, want); found != nil {
				return found
			}
		}
	case []any:
		for _, x := range v {
			if found := findObject(x, key, want); found != nil {
				return found
			}
		}
	}
	return nil
}

func findString(value any, path ...string) string {
	if len(path) == 0 {
		s, _ := value.(string)
		return s
	}
	switch v := value.(type) {
	case map[string]any:
		if x, ok := v[path[0]]; ok {
			if s := findString(x, path[1:]...); s != "" {
				return s
			}
		}
		for _, x := range v {
			if s := findString(x, path...); s != "" {
				return s
			}
		}
	case []any:
		for _, x := range v {
			if s := findString(x, path...); s != "" {
				return s
			}
		}
	}
	return ""
}

func withReadback(result, readback Result) Result {
	if readback.OK {
		result.Readback = readback.Data
		result.Verification = "readback_only"
	} else {
		result.Partial = true
		result.Verification = "unverified"
		result.Readback = readback.Error
	}
	// Reading a value is not proving it equals the entire intended change.
	return result
}
