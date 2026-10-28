# Feishu Project work items

These are Project requirements/bugs/tasks, not Approval `approve/reject` tasks.

1. `project_schema` decode_url uses official `url decode`; resolve the returned
   space shorthand with projects before using project_key. Do not guess keys
   from URL path segments; same-name projects need disambiguation.
2. Discover types, fields, roles and users. For an existing item, `ticket_get`
   returns its actual type. Never hardcode story/issue, priority or status values.
3. `ticket_search` takes project_key/work_item_type and optional query, status,
   assignee=me, limit (1–50). It compiles a bounded escaped MQL statement. Status
   must come from real schema options. Pagination uses session_id and
   group_pagination_list from the previous result, not LIMIT offset.
4. `ticket_update` takes exact project_key/work_item_id and a fields array of
   `{field_key, field_value}`. The wrapper serializes non-string values into the
   Project string protocol; user IDs must be Project user_keys, not Lark open_ids.
   Use expected field values for optimistic conflict detection where available.
5. Status transitions: discover transitions and required fields first; use
   ticket_transition state with transition_id, or node with node_id. No automatic
   rollback or batch closure. Missing fields/roles stop execution.
6. Readback is returned without claiming full equivalence verification. Compare
   the specific fields/status before saying the requested modification is done.

```json
{"operation":"search","options":{"project_key":"actual-project-key","work_item_type":"actual-type-key","query":"登录失败","limit":20}}
```

```json
{"options":{"project_key":"actual-project-key","work_item_id":"12345","fields":[{"field_key":"name","field_value":"修复登录重定向"}]},"confirm":true}
```
