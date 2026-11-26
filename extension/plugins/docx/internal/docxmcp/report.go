package docxmcp

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
)

const (
	reportDefaultImagesPerRow = 2
	reportDefaultImageWidthPx = 180
	reportMaxSteps            = 200
	reportMaxImagesPerRow     = 4
	reportMaxImageWidthPx     = 2000
	reportMinImageWidthPx     = 16
)

type renderStep struct {
	Description string   `json:"description"`
	Images      []string `json:"images,omitempty"`
}

type renderData struct {
	PolicyNo string       `json:"policyNo"`
	Title    string       `json:"title"`
	Steps    []renderStep `json:"steps"`
}

type renderParams struct {
	Path         string     `json:"path"`
	Output       string     `json:"output"`
	Data         renderData `json:"data"`
	ImagesRoot   string     `json:"images_root,omitempty"`
	ImagesPerRow int        `json:"images_per_row,omitempty"`
	ImageWidthPx int        `json:"image_width_px,omitempty"`
	Overwrite    bool       `json:"overwrite,omitempty"`
}

type renderResult struct {
	Path     string   `json:"path"`
	Steps    int      `json:"steps"`
	Images   int      `json:"images"`
	Warnings []string `json:"warnings,omitempty"`
	Bytes    int      `json:"bytes"`
}

type reportImage struct {
	data        []byte
	width       int
	height      int
	relID       string
	media       string // word/media/...
	mediaTarget string // media/...
}

func (p *Plugin) render(ctx context.Context, raw json.RawMessage) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var params renderParams
	if err := decodeArgs(raw, &params); err != nil {
		return nil, err
	}
	perRow := params.ImagesPerRow
	if perRow == 0 {
		perRow = reportDefaultImagesPerRow
	}
	if perRow < 1 || perRow > reportMaxImagesPerRow {
		return nil, errf(codeInvalidArguments, "images_per_row must be 1..%d", reportMaxImagesPerRow)
	}
	width := params.ImageWidthPx
	if width == 0 {
		width = reportDefaultImageWidthPx
	}
	if width < reportMinImageWidthPx || width > reportMaxImageWidthPx {
		return nil, errf(codeInvalidArguments, "image_width_px must be %d..%d", reportMinImageWidthPx, reportMaxImageWidthPx)
	}
	if len(params.Data.Steps) > reportMaxSteps {
		return nil, errf(codeInvalidArguments, "at most %d steps are accepted", reportMaxSteps)
	}
	totalImages, totalText := 0, len(params.Data.PolicyNo)+len(params.Data.Title)
	for _, step := range params.Data.Steps {
		totalImages += len(step.Images)
		totalText += len(step.Description)
	}
	if totalImages > maxImages {
		return nil, errf(codeImageLimit, "at most %d images are accepted per document", maxImages)
	}
	if totalText > maxTextBytes {
		return nil, errf(codeInvalidArguments, "text exceeds the %d MiB limit", maxTextBytes>>20)
	}
	imagesRoot, err := cleanImagesRoot(params.ImagesRoot)
	if err != nil {
		return nil, err
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
	docXML, err := partBytes(inspected, "word/document.xml", maxXMLBytes)
	if err != nil {
		return nil, err
	}
	root, err := parseXMLTree(docXML)
	if err != nil {
		return nil, err
	}
	body := root.firstDescendant("body")
	if body == nil {
		return nil, errf(codeInvalidDocx, "document body is missing")
	}
	table := body.firstDescendant("tbl")
	if table == nil {
		return nil, errf(codeInvalidArguments, "template must contain the 2-column step table")
	}
	rows := table.direct("tr")
	if len(rows) < 3 {
		return nil, errf(codeInvalidArguments, "template table needs a policy row, a title row and one step row")
	}
	rowCells := func(row *xmlElem) []*xmlElem { return row.direct("tc") }
	if len(rowCells(rows[0])) < 2 || len(rowCells(rows[1])) < 1 || len(rowCells(rows[2])) < 2 {
		return nil, errf(codeInvalidArguments, "template table must have 2 columns and one prototype step row")
	}
	prototype := rows[2]
	prototypeDrawing := rowCells(prototype)[1].firstDescendant("drawing")
	if prototypeDrawing == nil {
		return nil, errf(codeInvalidArguments, "the prototype step row has no image placeholder")
	}
	prototypeDrawingXML := rawBytes(docXML, prototypeDrawing)

	// Load and dedupe images in step order. A repeated path reuses its media
	// part and relationship, so one screenshot in many steps costs one entry.
	images := make([]*reportImage, 0, totalImages)
	byKey := make(map[string]*reportImage)
	for _, step := range params.Data.Steps {
		for _, imagePath := range step.Images {
			clean, err := cleanRelative(imagePath)
			if err != nil {
				return nil, err
			}
			key := path.Join(imagesRoot, clean)
			if img, ok := byKey[key]; ok {
				images = append(images, img)
				continue
			}
			loaded, err := p.loadImageAt(clean, imagesRoot)
			if err != nil {
				return nil, err
			}
			img := &reportImage{data: loaded.data, width: loaded.width, height: loaded.height}
			byKey[key] = img
			images = append(images, img)
		}
	}

	relsXML, err := partBytes(inspected, "word/_rels/document.xml.rels", maxXMLBytes)
	if err != nil {
		return nil, err
	}
	relsRoot, err := parseXMLTree(relsXML)
	if err != nil {
		return nil, err
	}
	nextRID := 0
	for _, rel := range relsRoot.descendants("Relationship") {
		if n, ok := relIDNumber(xmlAttr(relsXML, rel, "Id")); ok && n > nextRID {
			nextRID = n
		}
	}
	usedMedia := make(map[string]bool, len(inspected.files))
	for name := range inspected.files {
		usedMedia[strings.ToLower(name)] = true
	}
	mediaSeq := 0
	for _, step := range params.Data.Steps {
		for _, imagePath := range step.Images {
			clean, _ := cleanRelative(imagePath)
			img := byKey[path.Join(imagesRoot, clean)]
			if img == nil || img.relID != "" {
				continue
			}
			nextRID++
			img.relID = fmt.Sprintf("rId%d", nextRID)
			for {
				mediaSeq++
				name := fmt.Sprintf("word/media/golder-render-%d.png", mediaSeq)
				if !usedMedia[strings.ToLower(name)] {
					usedMedia[strings.ToLower(name)] = true
					img.media = name
					img.mediaTarget = strings.TrimPrefix(name, "word/")
					break
				}
			}
		}
	}
	newRels, err := appendImageRelationships(relsXML, images)
	if err != nil {
		return nil, err
	}
	contentTypes, err := partBytes(inspected, "[Content_Types].xml", maxXMLBytes)
	if err != nil {
		return nil, err
	}
	newContentTypes, err := ensurePNGContentType(contentTypes)
	if err != nil {
		return nil, err
	}

	cellWidth := tableCellWidthPx(docXML, table, rowCells(prototype)[1])
	docPrID := maxDocPrID(docXML)
	docPrID++

	header := map[int][]byte{
		1: rebuildTextCell(docXML, rowCells(rows[0])[1], params.Data.PolicyNo),
	}
	headerRow := rebuildRow(docXML, rows[0], header)
	titleRow := rebuildRow(docXML, rows[1], map[int][]byte{
		0: rebuildTextCell(docXML, rowCells(rows[1])[0], params.Data.Title),
	})
	newRows := [][]byte{headerRow, titleRow}
	imageIndex := 0
	for _, step := range params.Data.Steps {
		stepImages := images[imageIndex : imageIndex+len(step.Images)]
		imageIndex += len(step.Images)
		imageCell := rebuildImageCell(docXML, rowCells(prototype)[1], stepImages, prototypeDrawingXML, perRow, width, cellWidth, &docPrID)
		row := rebuildRow(docXML, prototype, map[int][]byte{
			0: rebuildTextCell(docXML, rowCells(prototype)[0], step.Description),
			1: imageCell,
		})
		newRows = append(newRows, row)
	}
	newDocXML := replaceTable(docXML, table, newRows)
	if len(newDocXML) > maxXMLBytes {
		return nil, errf(codeXMLLimit, "rendered document XML exceeds the %d MiB limit", maxXMLBytes>>20)
	}
	media := make(map[string][]byte, len(images))
	for _, img := range images {
		if img.media != "" {
			media[img.media] = img.data
		}
	}
	out, err := rebuildPackage(data, newDocXML, newRels, newContentTypes, media)
	if err != nil {
		return nil, err
	}
	if _, err := inspectDocx(out); err != nil {
		return nil, errf(codeWriteFailed, "rendered document failed validation: %v", err)
	}
	if err := WriteAtomic(target, out); err != nil {
		return nil, err
	}
	result := renderResult{Path: target, Steps: len(params.Data.Steps), Images: totalImages, Bytes: len(out)}
	if params.Data.PolicyNo == "" {
		result.Warnings = append(result.Warnings, "policyNo is empty")
	}
	if params.Data.Title == "" {
		result.Warnings = append(result.Warnings, "title is empty")
	}
	for i, step := range params.Data.Steps {
		if step.Description == "" {
			result.Warnings = append(result.Warnings, fmt.Sprintf("step %d description is empty", i+1))
		}
	}
	return result, nil
}

func cleanImagesRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" || root == "." {
		return ".", nil
	}
	clean, err := cleanRelative(root)
	if err != nil {
		return "", err
	}
	return clean, nil
}

func relIDNumber(id string) (int, bool) {
	if !strings.HasPrefix(id, "rId") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(id, "rId"))
	return n, err == nil && n > 0
}

func rawBytes(data []byte, n *xmlElem) []byte {
	if n == nil || n.end < n.start || n.end > len(data) {
		return nil
	}
	return append([]byte(nil), data[n.start:n.end]...)
}

func rebuildTextCell(data []byte, cell *xmlElem, text string) []byte {
	var b bytes.Buffer
	b.WriteString("<w:tc>")
	if tcPr := cell.firstDirect("tcPr"); tcPr != nil {
		b.Write(data[tcPr.start:tcPr.end])
	}
	var pPr, rPr []byte
	if p := cell.firstDirect("p"); p != nil {
		if n := p.firstDirect("pPr"); n != nil {
			pPr = data[n.start:n.end]
		}
		if r := p.firstDirect("r"); r != nil {
			if n := r.firstDirect("rPr"); n != nil {
				rPr = data[n.start:n.end]
			}
		}
	}
	b.WriteString("<w:p>")
	b.Write(pPr)
	b.WriteString("<w:r>")
	b.Write(rPr)
	b.WriteString(`<w:t xml:space="preserve">`)
	b.WriteString(xmlEscapeText(text))
	b.WriteString(`</w:t></w:r></w:p></w:tc>`)
	return b.Bytes()
}

func rebuildImageCell(data []byte, cell *xmlElem, images []*reportImage, drawingXML []byte, perRow, width, cellPx int, docPrID *int) []byte {
	var b bytes.Buffer
	b.WriteString("<w:tc>")
	if tcPr := cell.firstDirect("tcPr"); tcPr != nil {
		b.Write(data[tcPr.start:tcPr.end])
	}
	var pPr []byte
	if p := cell.firstDirect("p"); p != nil {
		if n := p.firstDirect("pPr"); n != nil {
			pPr = removeElement(data[n.start:n.end], "numPr")
		}
	}
	if len(images) == 0 {
		b.WriteString("<w:p>")
		b.Write(pPr)
		b.WriteString("</w:p></w:tc>")
		return b.Bytes()
	}
	maxWidth := width
	if cellPx > 0 {
		if fit := (cellPx - 12) / perRow; fit > 0 && fit < maxWidth {
			maxWidth = fit
		}
	}
	for start := 0; start < len(images); start += perRow {
		b.WriteString("<w:p>")
		b.Write(pPr)
		end := min(start+perRow, len(images))
		for i := start; i < end; i++ {
			if i > start {
				b.WriteString(`<w:r><w:t xml:space="preserve"> </w:t></w:r>`)
			}
			img := images[i]
			h := maxWidth
			if img.width > 0 && img.height > 0 {
				h = max(1, img.height*maxWidth/img.width)
			}
			*docPrID++
			b.WriteString("<w:r>")
			b.Write(drawingWithImage(drawingXML, img.relID, maxWidth, h, *docPrID))
			b.WriteString("</w:r>")
		}
		b.WriteString("</w:p>")
	}
	b.WriteString("</w:tc>")
	return b.Bytes()
}

func drawingWithImage(drawing []byte, relID string, widthPx, heightPx, docPr int) []byte {
	out := append([]byte(nil), drawing...)
	cx, cy := strconv.Itoa(widthPx*9525), strconv.Itoa(heightPx*9525)
	out = setTagAttrs(out, "wp:extent", map[string]string{"cx": cx, "cy": cy})
	out = setTagAttrs(out, "a:ext", map[string]string{"cx": cx, "cy": cy})
	out = setTagAttrs(out, "wp:docPr", map[string]string{"id": strconv.Itoa(docPr)})
	out = setTagAttrs(out, "pic:cNvPr", map[string]string{"id": strconv.Itoa(docPr)})
	return regexp.MustCompile(`r:embed="[^"]*"`).ReplaceAll(out, []byte(`r:embed="`+relID+`"`))
}

func setTagAttrs(raw []byte, tag string, attrs map[string]string) []byte {
	needle := []byte("<" + tag)
	var out bytes.Buffer
	rest := raw
	for {
		i := bytes.Index(rest, needle)
		if i < 0 {
			out.Write(rest)
			break
		}
		j := i + len(needle)
		if j < len(rest) && !xmlSpace(rest[j]) && rest[j] != '>' && rest[j] != '/' {
			out.Write(rest[:j])
			rest = rest[j:]
			continue
		}
		gt, err := xmlTagEnd(rest, j)
		if err != nil {
			out.Write(rest)
			break
		}
		out.Write(rest[:i])
		out.Write(setAttrsInTag(rest[i:gt+1], attrs))
		rest = rest[gt+1:]
	}
	return out.Bytes()
}

func setAttrsInTag(tag []byte, attrs map[string]string) []byte {
	s := string(tag)
	for name, value := range attrs {
		re := regexp.MustCompile(`(?i)(\s` + regexp.QuoteMeta(name) + `=")[^"]*(")`)
		escaped := xmlEscapeText(value)
		if re.MatchString(s) {
			s = re.ReplaceAllString(s, `${1}`+escaped+`${2}`)
			continue
		}
		at := strings.LastIndex(s, "/>")
		closing := "/>"
		if at < 0 {
			at, closing = strings.LastIndex(s, ">"), ">"
		}
		if at < 0 {
			continue
		}
		s = s[:at] + " " + name + `="` + escaped + `"` + closing + s[at+len(closing):]
	}
	return []byte(s)
}

func removeElement(raw []byte, name string) []byte {
	root, err := parseXMLTree(raw)
	if err != nil {
		return raw
	}
	n := root.firstDescendant(name)
	if n == nil {
		return raw
	}
	out := make([]byte, 0, len(raw)-(n.end-n.start))
	out = append(out, raw[:n.start]...)
	out = append(out, raw[n.end:]...)
	return out
}

func rebuildRow(data []byte, row *xmlElem, replacements map[int][]byte) []byte {
	var b bytes.Buffer
	b.Write(data[row.start:row.startEnd])
	cellIndex := 0
	for _, child := range row.children {
		if child.name == "tc" {
			if replacement, ok := replacements[cellIndex]; ok {
				b.Write(replacement)
			} else {
				b.Write(data[child.start:child.end])
			}
			cellIndex++
			continue
		}
		b.Write(data[child.start:child.end])
	}
	b.Write(data[row.endStart:row.end])
	return b.Bytes()
}

func replaceTable(data []byte, table *xmlElem, rows [][]byte) []byte {
	var b bytes.Buffer
	b.Write(data[table.start:table.startEnd])
	wroteRows := false
	for _, child := range table.children {
		if child.name == "tr" {
			if !wroteRows {
				for _, row := range rows {
					b.Write(row)
				}
				wroteRows = true
			}
			continue
		}
		b.Write(data[child.start:child.end])
	}
	b.Write(data[table.endStart:table.end])
	return b.Bytes()
}

func tableCellWidthPx(data []byte, table, cell *xmlElem) int {
	twips := 0
	if grid := table.firstDirect("tblGrid"); grid != nil {
		for _, col := range grid.direct("gridCol") {
			if n, err := strconv.Atoi(xmlAttr(data, col, "w")); err == nil {
				twips += n
			}
		}
	}
	if tcPr := cell.firstDirect("tcPr"); tcPr != nil {
		if tcW := tcPr.firstDirect("tcW"); tcW != nil {
			w, _ := strconv.Atoi(xmlAttr(data, tcW, "w"))
			switch xmlAttr(data, tcW, "type") {
			case "pct":
				twips = twips * w / 5000
			default:
				twips = w
			}
		}
	}
	return twips / 15 // 1440 twips per inch at 96 DPI
}

func maxDocPrID(data []byte) int {
	maxID := 0
	re := regexp.MustCompile(`<wp:docPr\b[^>]*\bid="([0-9]+)"`)
	for _, m := range re.FindAllSubmatch(data, -1) {
		if n, err := strconv.Atoi(string(m[1])); err == nil && n > maxID {
			maxID = n
		}
	}
	return maxID
}

func appendImageRelationships(rels []byte, images []*reportImage) ([]byte, error) {
	idx := bytes.LastIndex(rels, []byte("</Relationships>"))
	if idx < 0 {
		return nil, errf(codeInvalidDocx, "invalid document relationships")
	}
	var add bytes.Buffer
	seen := make(map[string]bool)
	for _, img := range images {
		if img.relID == "" || seen[img.relID] {
			continue
		}
		seen[img.relID] = true
		fmt.Fprintf(&add, `<Relationship Id="%s" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="%s"/>`, img.relID, xmlEscapeText(img.mediaTarget))
	}
	out := make([]byte, 0, len(rels)+add.Len())
	out = append(out, rels[:idx]...)
	out = append(out, add.Bytes()...)
	out = append(out, rels[idx:]...)
	return out, nil
}

func ensurePNGContentType(ct []byte) ([]byte, error) {
	if regexp.MustCompile(`(?i)<Default[^>]*\bExtension="png"`).Match(ct) {
		return ct, nil
	}
	idx := bytes.LastIndex(ct, []byte("</Types>"))
	if idx < 0 {
		return nil, errf(codeInvalidDocx, "invalid [Content_Types].xml")
	}
	insert := []byte(`<Default Extension="png" ContentType="image/png"/>`)
	out := make([]byte, 0, len(ct)+len(insert))
	out = append(out, ct[:idx]...)
	out = append(out, insert...)
	out = append(out, ct[idx:]...)
	return out, nil
}

func rebuildPackage(data, document, rels, contentTypes []byte, media map[string][]byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errf(codeInvalidDocx, "not a valid DOCX zip package")
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		body := []byte(nil)
		switch f.Name {
		case "word/document.xml":
			body = document
		case "word/_rels/document.xml.rels":
			body = rels
		case "[Content_Types].xml":
			body = contentTypes
		default:
			rc, err := f.Open()
			if err != nil {
				return nil, errf(codeReadFailed, "cannot read package part")
			}
			body, err = io.ReadAll(io.LimitReader(rc, maxEntryBytes+1))
			rc.Close()
			if err != nil || int64(len(body)) > maxEntryBytes {
				return nil, errf(codeReadFailed, "cannot read package part")
			}
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: f.Method, Modified: f.Modified})
		if err != nil {
			return nil, errf(codeWriteFailed, "cannot write package part")
		}
		if _, err := w.Write(body); err != nil {
			return nil, errf(codeWriteFailed, "cannot write package part")
		}
	}
	for name, body := range media {
		w, err := zw.Create(name)
		if err != nil {
			return nil, errf(codeWriteFailed, "cannot write image part")
		}
		if _, err := w.Write(body); err != nil {
			return nil, errf(codeWriteFailed, "cannot write image part")
		}
	}
	if err := zw.Close(); err != nil {
		return nil, errf(codeWriteFailed, "cannot finish package")
	}
	return buf.Bytes(), nil
}
