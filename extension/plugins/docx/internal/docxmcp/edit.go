package docxmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/mmonterroca/docxgo/v2"
	"github.com/mmonterroca/docxgo/v2/domain"
	"github.com/mmonterroca/docxgo/v2/pkg/template"
)

type editParams struct {
	Path         string            `json:"path"`
	Output       string            `json:"output"`
	Replacements map[string]string `json:"replacements,omitempty"`
	Append       []blockSpec       `json:"append,omitempty"`
	Overwrite    bool              `json:"overwrite,omitempty"`
}

type editResult struct {
	Path     string `json:"path"`
	Replaced int    `json:"replaced"`
	Skipped  int    `json:"skipped"`
	Appended int    `json:"appended"`
	Images   int    `json:"images"`
	Bytes    int    `json:"bytes"`
}

func (p *Plugin) edit(ctx context.Context, raw json.RawMessage) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var params editParams
	if err := decodeArgs(raw, &params); err != nil {
		return nil, err
	}
	if len(params.Replacements) == 0 && len(params.Append) == 0 {
		return nil, errf(codeInvalidArguments, "replacements or append is required")
	}
	if len(params.Replacements) > maxReplacements {
		return nil, errf(codeInvalidArguments, "at most %d replacements are accepted", maxReplacements)
	}
	total := 0
	for find, replace := range params.Replacements {
		if find == "" {
			return nil, errf(codeInvalidArguments, "replacement keys must not be empty")
		}
		total += len(find) + len(replace)
	}
	if total > maxTextBytes {
		return nil, errf(codeInvalidArguments, "replacement text exceeds the %d MiB limit", maxTextBytes>>20)
	}
	if len(params.Append) > 0 {
		if err := validateBlocks(params.Append); err != nil {
			return nil, err
		}
	}
	target, err := p.ws.PrepareOutput(params.Output, params.Overwrite)
	if err != nil {
		return nil, err
	}
	_, data, err := p.ws.ReadDocx(params.Path)
	if err != nil {
		return nil, err
	}
	inspected, err := inspectDocx(data)
	if err != nil {
		return nil, err
	}
	doc, err := docx.OpenDocumentFromBytes(inspected.data)
	if err != nil {
		return nil, errf(codeReadFailed, "cannot parse document structure: %v", err)
	}
	keys := make([]string, 0, len(params.Replacements))
	for key := range params.Replacements {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := editResult{Path: target}
	for _, find := range keys {
		res, err := template.ReplaceText(doc, find, params.Replacements[find])
		if err != nil {
			return nil, errf(codeWriteFailed, "cannot replace text: %v", err)
		}
		result.Replaced += res.Replaced
		result.Skipped += res.Skipped
	}
	for _, spec := range params.Append {
		if err := p.appendDocumentBlock(doc, spec); err != nil {
			return nil, err
		}
		result.Appended++
		if spec.Type == "image" {
			result.Images++
		}
	}
	var buf bytes.Buffer
	if _, err := doc.WriteTo(&buf); err != nil {
		return nil, errf(codeWriteFailed, "cannot serialize document: %v", err)
	}
	if err := WriteAtomic(target, buf.Bytes()); err != nil {
		return nil, err
	}
	result.Bytes = buf.Len()
	return result, nil
}

func (p *Plugin) appendDocumentBlock(doc domain.Document, spec blockSpec) error {
	switch spec.Type {
	case "heading":
		para, err := doc.AddParagraph()
		if err != nil {
			return errf(codeWriteFailed, "cannot add heading: %v", err)
		}
		if err := setParagraphText(para, spec.Text); err != nil {
			return err
		}
		_ = para.SetStyle(headingStyle(spec.Level))
		if runs := para.Runs(); len(runs) > 0 {
			_ = runs[len(runs)-1].SetBold(true)
			_ = runs[len(runs)-1].SetSize(headingPoints(spec.Level) * 2)
		}
	case "paragraph":
		para, err := doc.AddParagraph()
		if err != nil {
			return errf(codeWriteFailed, "cannot add paragraph: %v", err)
		}
		if err := setParagraphText(para, spec.Text); err != nil {
			return err
		}
	case "bullets":
		for _, item := range spec.Items {
			para, err := doc.AddParagraph()
			if err != nil {
				return errf(codeWriteFailed, "cannot add bullet: %v", err)
			}
			if err := setParagraphText(para, "• "+item); err != nil {
				return err
			}
		}
	case "table":
		table, err := doc.AddTable(len(spec.Rows), len(spec.Rows[0]))
		if err != nil {
			return errf(codeWriteFailed, "cannot add table: %v", err)
		}
		for i, row := range spec.Rows {
			tableRow, err := table.Row(i)
			if err != nil {
				return errf(codeWriteFailed, "cannot add table row: %v", err)
			}
			for j, text := range row {
				cell, err := tableRow.Cell(j)
				if err != nil {
					return errf(codeWriteFailed, "cannot add table cell: %v", err)
				}
				para, err := cell.AddParagraph()
				if err != nil {
					return errf(codeWriteFailed, "cannot add cell paragraph: %v", err)
				}
				if err := setParagraphText(para, text); err != nil {
					return err
				}
			}
		}
	case "image":
		asset, err := p.loadImage(spec.Path)
		if err != nil {
			return err
		}
		w, h := spec.WidthPx, spec.HeightPx
		if w <= 0 || h <= 0 {
			w, h = asset.width, asset.height
		}
		para, err := doc.AddParagraph()
		if err != nil {
			return errf(codeWriteFailed, "cannot add image paragraph: %v", err)
		}
		if _, err := para.AddImageFromBytesWithSize(asset.data, asset.format, domain.NewImageSize(w, h)); err != nil {
			return errf(codeWriteFailed, "cannot add image: %v", err)
		}
	}
	return nil
}

func setParagraphText(para domain.Paragraph, text string) error {
	run, err := para.AddRun()
	if err != nil {
		return errf(codeWriteFailed, "cannot add run: %v", err)
	}
	if err := run.SetText(strings.TrimSpace(text)); err != nil {
		return errf(codeWriteFailed, "cannot set run text: %v", err)
	}
	return nil
}
