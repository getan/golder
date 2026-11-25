package docxmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/mmonterroca/docxgo/v2"
	"github.com/mmonterroca/docxgo/v2/domain"
)

type blockSpec struct {
	Type     string     `json:"type"`
	Level    int        `json:"level,omitempty"`
	Text     string     `json:"text,omitempty"`
	Items    []string   `json:"items,omitempty"`
	Rows     [][]string `json:"rows,omitempty"`
	Header   bool       `json:"header,omitempty"`
	Path     string     `json:"path,omitempty"`
	WidthPx  int        `json:"width_px,omitempty"`
	HeightPx int        `json:"height_px,omitempty"`
	Alt      string     `json:"alt,omitempty"`
}

type createParams struct {
	Path      string      `json:"path"`
	Title     string      `json:"title,omitempty"`
	Blocks    []blockSpec `json:"blocks"`
	Overwrite bool        `json:"overwrite,omitempty"`
}

type writeResult struct {
	Path       string `json:"path"`
	Paragraphs int    `json:"paragraphs"`
	Tables     int    `json:"tables"`
	Images     int    `json:"images"`
	Bytes      int    `json:"bytes"`
}

func validateBlocks(blocks []blockSpec) error {
	if len(blocks) == 0 {
		return errf(codeInvalidArguments, "blocks is required")
	}
	if len(blocks) > maxBlocks {
		return errf(codeInvalidArguments, "at most %d blocks are accepted", maxBlocks)
	}
	totalText, totalImages, totalCells := 0, 0, 0
	for _, b := range blocks {
		switch b.Type {
		case "heading":
			if b.Level < 1 || b.Level > 6 {
				return errf(codeInvalidArguments, "heading level must be 1..6")
			}
			if strings.TrimSpace(b.Text) == "" {
				return errf(codeInvalidArguments, "heading text is required")
			}
			totalText += len(b.Text)
		case "paragraph":
			if strings.TrimSpace(b.Text) == "" {
				return errf(codeInvalidArguments, "paragraph text is required")
			}
			totalText += len(b.Text)
		case "bullets":
			if len(b.Items) == 0 || len(b.Items) > 500 {
				return errf(codeInvalidArguments, "bullets require 1..500 items")
			}
			for _, item := range b.Items {
				totalText += len(item)
			}
		case "table":
			if len(b.Rows) == 0 || len(b.Rows) > maxTableRows {
				return errf(codeInvalidArguments, "table requires 1..%d rows", maxTableRows)
			}
			cols := len(b.Rows[0])
			if cols == 0 || cols > maxTableCols {
				return errf(codeInvalidArguments, "table requires 1..%d columns", maxTableCols)
			}
			for _, row := range b.Rows {
				if len(row) != cols {
					return errf(codeInvalidArguments, "every table row must have %d cells", cols)
				}
				for _, cell := range row {
					totalText += len(cell)
				}
			}
			totalCells += len(b.Rows) * cols
			if totalCells > maxTableCells {
				return errf(codeInvalidArguments, "table cell count exceeds %d", maxTableCells)
			}
		case "image":
			if strings.TrimSpace(b.Path) == "" {
				return errf(codeInvalidArguments, "image path is required")
			}
			totalImages++
			if totalImages > maxImages {
				return errf(codeImageLimit, "at most %d images are accepted per document", maxImages)
			}
		default:
			return errf(codeInvalidArguments, "unsupported block type %q", b.Type)
		}
		if totalText > maxTextBytes {
			return errf(codeInvalidArguments, "text exceeds the %d MiB limit", maxTextBytes>>20)
		}
	}
	return nil
}

func (p *Plugin) create(ctx context.Context, raw json.RawMessage) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var params createParams
	if err := decodeArgs(raw, &params); err != nil {
		return nil, err
	}
	if err := validateBlocks(params.Blocks); err != nil {
		return nil, err
	}
	target, err := p.ws.PrepareOutput(params.Path, params.Overwrite)
	if err != nil {
		return nil, err
	}
	doc := docx.NewDocument()
	if err := doc.SetMetadata(&domain.Metadata{
		Title:   strings.TrimSpace(params.Title),
		Creator: "golder-docx",
	}); err != nil {
		return nil, errf(codeWriteFailed, "cannot set document metadata: %v", err)
	}
	for _, spec := range params.Blocks {
		if err := p.appendDocumentBlock(doc, spec); err != nil {
			return nil, err
		}
	}
	var buf bytes.Buffer
	if _, err := doc.WriteTo(&buf); err != nil {
		return nil, errf(codeWriteFailed, "cannot serialize document: %v", err)
	}
	if err := WriteAtomic(target, buf.Bytes()); err != nil {
		return nil, err
	}
	return writeResult{Path: target, Paragraphs: countParagraphBlocks(params.Blocks), Tables: countTables(params.Blocks), Images: countImages(params.Blocks), Bytes: buf.Len()}, nil
}

func countParagraphBlocks(blocks []blockSpec) int {
	n := 0
	for _, b := range blocks {
		switch b.Type {
		case "paragraph", "heading":
			n++
		case "bullets":
			n += len(b.Items)
		}
	}
	return n
}

func countTables(blocks []blockSpec) int {
	n := 0
	for _, b := range blocks {
		if b.Type == "table" {
			n++
		}
	}
	return n
}

func countImages(blocks []blockSpec) int {
	n := 0
	for _, b := range blocks {
		if b.Type == "image" {
			n++
		}
	}
	return n
}
