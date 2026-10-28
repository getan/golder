# Sheets and Base

## Sheets

- `sheet_read` defaults to workbook metadata. Resolve the real, visible grid
  sheet; use sheet_id or sheet_name and a bounded A1 range for values/cells.
- Raw `cells` preserve precision/formulas; CSV is display text and can collapse
  long IDs to scientific notation. Do not compare identifiers from display CSV.
- `sheet_write` cells accepts a 2D array of typed cell objects; table accepts
  `{sheets:[{name,mode,start_cell,columns,data,dtypes,formats,...}]}`. Explicitly
  choose append or overwrite. Overwrite requires allow_overwrite=true.
- Preserve leading zeroes as text, and explicit number/date/percentage dtypes.
  Formula errors or pending calculation are not success. Preserve unrelated
  regions, styles and hidden data. Never claim a sampled read proves all rows.
- `sheet_structure` create takes title; insert takes position (row number or
  column letter) and count. No automatic clear, sheet deletion or rollback.

## Base

- Resolve URL/title, then table and real field schema. Handle ambiguous matches.
- Search uses a bounded inline JSON filter/query body, with offset/limit where
  supported. Respect returned partial/pagination/ignored_fields information.
- Create JSON: `{create_records:[{FieldName:value},...]}`.
- Update JSON: `{update_records:{recordID:{FieldName:value},...}}`.
- At most 200 records per call, same-table serial. Use real select/user/relation
  values; do not write formula, lookup, system or attachment fields as CellValue.
- Delete needs one exact record_id plus operator approval. Bulk delete,
  permission changes and Workflow enablement are not a generic fallback.
- Readback may be sampled; inspect the verification field and do not replay an
  entire batch after a partial/unknown result.
