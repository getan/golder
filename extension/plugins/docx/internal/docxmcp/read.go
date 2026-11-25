package docxmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"io"
	"path"
	"strconv"
	"strings"
)

type readParams struct {
	Path     string `json:"path"`
	Format   string `json:"format,omitempty"`
	MaxChars int    `json:"max_chars,omitempty"`
}

type imageInfo struct {
	Format string `json:"format"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Alt    string `json:"alt,omitempty"`
}

type readResult struct {
	Path       string      `json:"path"`
	Format     string      `json:"format"`
	Text       string      `json:"text"`
	Chars      int         `json:"chars"`
	Truncated  bool        `json:"truncated"`
	Paragraphs int         `json:"paragraphs"`
	Tables     int         `json:"tables"`
	Images     []imageInfo `json:"images,omitempty"`
	ImageCount int         `json:"image_count"`
}

type xmlNode struct {
	Name     xml.Name
	Attr     []xml.Attr
	Text     string
	Children []*xmlNode
}

type bodyBlock struct {
	paragraph bool
	text      string
	style     string
	rows      [][]string
	images    []string
}

func (p *Plugin) read(ctx context.Context, raw json.RawMessage) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var params readParams
	if err := decodeArgs(raw, &params); err != nil {
		return nil, err
	}
	format := strings.ToLower(strings.TrimSpace(params.Format))
	if format == "" {
		format = "text"
	}
	switch format {
	case "text", "outline", "markdown":
	default:
		return nil, errf(codeInvalidArguments, "format must be text, outline or markdown")
	}
	limit := params.MaxChars
	if limit <= 0 {
		limit = defaultReadChars
	}
	if limit > maxReadChars {
		limit = maxReadChars
	}
	pathName, data, err := p.ws.ReadDocx(params.Path)
	if err != nil {
		return nil, err
	}
	inspected, err := inspectDocx(data)
	if err != nil {
		return nil, err
	}
	documentXML, err := partBytes(inspected, "word/document.xml", maxXMLBytes)
	if err != nil {
		return nil, err
	}
	root, err := parseXML(documentXML)
	if err != nil {
		return nil, errf(codeReadFailed, "cannot parse document body")
	}
	media := mediaRelations(inspected)
	blocks := bodyBlocks(root)
	out := readResult{Path: pathName, Format: format}
	var b strings.Builder
	for _, block := range blocks {
		if block.paragraph {
			out.Paragraphs++
			appendParagraph(&b, block, format)
		} else {
			out.Tables++
			if format == "outline" {
				continue
			}
			appendTable(&b, block.rows, format)
		}
		out.Images = append(out.Images, imageMetadata(inspected, media, block.images)...)
	}
	text := b.String()
	if len(text) > limit {
		text = truncateRunes(text, limit)
		out.Truncated = true
	}
	out.Text = text
	out.Chars = len([]rune(text))
	out.ImageCount = len(out.Images)
	if len(out.Images) > 64 {
		out.Images = out.Images[:64]
	}
	return out, nil
}

func partBytes(inspected *inspected, name string, limit int64) ([]byte, error) {
	f, ok := inspected.file(name)
	if !ok {
		return nil, errf(codeInvalidDocx, "required part %s is missing", name)
	}
	return readZipLimited(f, limit)
}

func parseXML(data []byte) (*xmlNode, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if start, ok := tok.(xml.StartElement); ok {
			return parseNode(dec, start)
		}
	}
}

func parseNode(dec *xml.Decoder, start xml.StartElement) (*xmlNode, error) {
	n := &xmlNode{Name: start.Name, Attr: append([]xml.Attr(nil), start.Attr...)}
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			child, err := parseNode(dec, t)
			if err != nil {
				return nil, err
			}
			n.Children = append(n.Children, child)
		case xml.CharData:
			n.Text += string(t)
		case xml.EndElement:
			return n, nil
		}
	}
}

func walk(n *xmlNode, fn func(*xmlNode)) {
	fn(n)
	for _, child := range n.Children {
		walk(child, fn)
	}
}

func attr(n *xmlNode, local string) string {
	for _, a := range n.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

func bodyBlocks(root *xmlNode) []bodyBlock {
	body := findFirst(root, "body")
	if body == nil {
		return nil
	}
	var out []bodyBlock
	for _, child := range body.Children {
		switch child.Name.Local {
		case "p":
			out = append(out, paragraphBlock(child))
		case "tbl":
			out = append(out, tableBlock(child))
		}
	}
	return out
}

func findFirst(n *xmlNode, local string) *xmlNode {
	if n.Name.Local == local {
		return n
	}
	for _, child := range n.Children {
		if found := findFirst(child, local); found != nil {
			return found
		}
	}
	return nil
}

func paragraphBlock(n *xmlNode) bodyBlock {
	block := bodyBlock{paragraph: true, text: paragraphText(n)}
	walk(n, func(c *xmlNode) {
		switch c.Name.Local {
		case "pStyle":
			block.style = attr(c, "val")
		case "blip", "imagedata":
			if id := imageRelID(c); id != "" {
				block.images = append(block.images, id)
			}
		}
	})
	return block
}

func paragraphText(n *xmlNode) string {
	var b strings.Builder
	walk(n, func(c *xmlNode) {
		switch c.Name.Local {
		case "t":
			b.WriteString(c.Text)
		case "tab":
			b.WriteString("\t")
		case "br", "cr":
			b.WriteString("\n")
		}
	})
	return strings.TrimSpace(b.String())
}

func tableBlock(n *xmlNode) bodyBlock {
	block := bodyBlock{rows: [][]string{}}
	for _, row := range n.Children {
		if row.Name.Local != "tr" {
			continue
		}
		var cells []string
		for _, cell := range row.Children {
			if cell.Name.Local != "tc" {
				continue
			}
			var parts []string
			for _, para := range cell.Children {
				if para.Name.Local == "p" {
					if text := paragraphText(para); text != "" {
						parts = append(parts, text)
					}
				}
			}
			cells = append(cells, strings.Join(parts, " "))
		}
		block.rows = append(block.rows, cells)
	}
	return block
}

func imageRelID(n *xmlNode) string {
	for _, a := range n.Attr {
		if a.Name.Local == "embed" || a.Name.Local == "id" {
			if strings.Contains(a.Name.Space, "relationships") || a.Name.Local == "embed" {
				return a.Value
			}
		}
	}
	return ""
}

func appendParagraph(b *strings.Builder, block bodyBlock, format string) {
	if block.text == "" {
		return
	}
	switch format {
	case "outline":
		level := styleLevel(block.style)
		if level == 0 {
			return
		}
		b.WriteString(strings.Repeat("  ", level-1))
		b.WriteString(block.text)
		b.WriteString("\n")
	case "markdown":
		if level := styleLevel(block.style); level > 0 {
			b.WriteString(strings.Repeat("#", level))
			b.WriteString(" ")
		}
		b.WriteString(block.text)
		b.WriteString("\n\n")
	default:
		b.WriteString(block.text)
		b.WriteString("\n")
	}
}

func appendTable(b *strings.Builder, rows [][]string, format string) {
	for i, cells := range rows {
		if format == "markdown" {
			b.WriteString("| ")
			b.WriteString(strings.Join(cells, " | "))
			b.WriteString(" |\n")
			if i == 0 {
				b.WriteString("|")
				for range cells {
					b.WriteString(" --- |")
				}
				b.WriteString("\n")
			}
		} else {
			b.WriteString(strings.Join(cells, "\t"))
			b.WriteString("\n")
		}
	}
}

func styleLevel(style string) int {
	style = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(style), " ", ""))
	if !strings.HasPrefix(style, "heading") {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimPrefix(style, "heading"))
	if err != nil || n < 1 || n > 9 {
		return 0
	}
	return n
}

func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}

func headingPoints(level int) int {
	switch level {
	case 1:
		return 24
	case 2:
		return 20
	case 3:
		return 16
	case 4:
		return 14
	case 5:
		return 13
	default:
		return 12
	}
}

func headingStyle(level int) string {
	return "Heading" + strconv.Itoa(level)
}

type relationships struct {
	Items []struct {
		ID         string `xml:"Id,attr"`
		Target     string `xml:"Target,attr"`
		TargetMode string `xml:"TargetMode,attr"`
	} `xml:"Relationship"`
}

func mediaRelations(inspected *inspected) map[string]string {
	data, err := partBytes(inspected, "word/_rels/document.xml.rels", maxXMLBytes)
	if err != nil {
		return nil
	}
	var rels relationships
	if err := xml.Unmarshal(data, &rels); err != nil {
		return nil
	}
	out := make(map[string]string, len(rels.Items))
	for _, rel := range rels.Items {
		if strings.EqualFold(rel.TargetMode, "External") || rel.ID == "" || rel.Target == "" {
			continue
		}
		target := strings.TrimPrefix(strings.ReplaceAll(rel.Target, `\`, "/"), "/")
		if target == "" || strings.HasPrefix(target, "../") || strings.Contains(target, "/../") {
			continue
		}
		if !strings.HasPrefix(target, "word/") {
			target = path.Join("word", target)
		}
		out[rel.ID] = target
	}
	return out
}

func imageMetadata(inspected *inspected, media map[string]string, ids []string) []imageInfo {
	var out []imageInfo
	for _, id := range ids {
		target, ok := media[id]
		if !ok {
			out = append(out, imageInfo{Format: "unknown"})
			continue
		}
		f, ok := inspected.file(target)
		if !ok {
			out = append(out, imageInfo{Format: "unknown"})
			continue
		}
		rc, err := f.Open()
		if err != nil {
			out = append(out, imageInfo{Format: "unknown"})
			continue
		}
		cfg, format, err := image.DecodeConfig(io.LimitReader(rc, maxImageBytes))
		_ = rc.Close()
		if err != nil {
			out = append(out, imageInfo{Format: "unknown"})
			continue
		}
		out = append(out, imageInfo{Format: format, Width: cfg.Width, Height: cfg.Height})
	}
	return out
}
