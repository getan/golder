package docxmcp

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const testDrawing = `<w:drawing><wp:inline distT="0" distB="0" distL="0" distR="0"><wp:extent cx="1714500" cy="3239770"/><wp:docPr id="5" name="Picture 5"/><a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture"><pic:pic><pic:nvPicPr><pic:cNvPr id="5" name="image1.png"/><pic:cNvPicPr/></pic:nvPicPr><pic:blipFill><a:blip r:embed="rId4"/><a:stretch><a:fillRect/></a:stretch></pic:blipFill><pic:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="1714500" cy="3239770"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></pic:spPr></pic:pic></a:graphicData></a:graphic></wp:inline></w:drawing>`

func reportFixture(t *testing.T, prototypeHasImage bool) []byte {
	t.Helper()
	prototypeImage := testDrawing
	if !prototypeHasImage {
		prototypeImage = ""
	}
	document := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"` +
		` xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"` +
		` xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"` +
		` xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"` +
		` xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture">` +
		`<w:body><w:tbl><w:tblPr><w:tblW w:w="0" w:type="auto"/></w:tblPr>` +
		`<w:tblGrid><w:gridCol w:w="2343"/><w:gridCol w:w="6176"/></w:tblGrid>` +
		`<w:tr><w:tc><w:tcPr><w:tcW w:w="1375" w:type="pct"/></w:tcPr><w:p><w:pPr><w:jc w:val="left"/></w:pPr><w:r><w:rPr><w:b/></w:rPr><w:t>Policy</w:t></w:r></w:p></w:tc>` +
		`<w:tc><w:tcPr><w:tcW w:w="3624" w:type="pct"/></w:tcPr><w:p><w:r><w:t>old-policy</w:t></w:r></w:p></w:tc></w:tr>` +
		`<w:tr><w:tc><w:tcPr><w:tcW w:w="5000" w:type="pct"/><w:gridSpan w:val="2"/></w:tcPr><w:p><w:r><w:t>old-title</w:t></w:r></w:p></w:tc></w:tr>` +
		`<w:tr><w:tc><w:tcPr><w:tcW w:w="1375" w:type="pct"/></w:tcPr><w:p><w:pPr><w:spacing w:before="157"/></w:pPr><w:r><w:rPr><w:b/></w:rPr><w:t>old-step</w:t></w:r></w:p></w:tc>` +
		`<w:tc><w:tcPr><w:tcW w:w="3624" w:type="pct"/></w:tcPr><w:p><w:pPr><w:snapToGrid/><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r>` + prototypeImage + `</w:r></w:p></w:tc></w:tr>` +
		`<w:tr><w:tc><w:tcPr><w:tcW w:w="1375" w:type="pct"/></w:tcPr><w:p><w:r><w:t>stale-step</w:t></w:r></w:p></w:tc>` +
		`<w:tc><w:tcPr><w:tcW w:w="3624" w:type="pct"/></w:tcPr><w:p/></w:tc></w:tr>` +
		`</w:tbl><w:sectPr/></w:body></w:document>`
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	add("[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8"?>`+
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`+
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>`+
		`<Default Extension="xml" ContentType="application/xml"/>`+
		`<Default Extension="png" ContentType="image/png"/>`+
		`<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>`+
		`</Types>`)
	add("_rels/.rels", `<?xml version="1.0" encoding="UTF-8"?>`+
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`+
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>`+
		`</Relationships>`)
	add("word/document.xml", document)
	add("word/_rels/document.xml.rels", `<?xml version="1.0" encoding="UTF-8"?>`+
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`+
		`<Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/image1.png"/>`+
		`</Relationships>`)
	add("word/media/image1.png", string(tinyPNG(t)))
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func renderFixture(t *testing.T, p *Plugin, dir string, params map[string]any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return p.render(context.Background(), raw)
}

func readDocxPart(t *testing.T, data []byte, name string) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		var b bytes.Buffer
		if _, err := b.ReadFrom(rc); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	t.Fatalf("part %s not found", name)
	return nil
}

func tableOf(t *testing.T, document []byte) (*xmlElem, []*xmlElem) {
	t.Helper()
	root, err := parseXMLTree(document)
	if err != nil {
		t.Fatal(err)
	}
	table := root.firstDescendant("tbl")
	if table == nil {
		t.Fatal("no table")
	}
	return table, table.direct("tr")
}

func cellText(t *testing.T, document []byte, cell *xmlElem) string {
	t.Helper()
	root, err := parseXMLTree(document[cell.start:cell.end])
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, n := range root.descendants("t") {
		b.WriteString(xmlUnescape(string(document[cell.start+n.startEnd : cell.start+n.endStart])))
	}
	return b.String()
}

func TestRenderClonesStepRowsAndPreservesFormatting(t *testing.T) {
	p, dir := newTestPlugin(t)
	writeFile(t, dir, "template.docx", reportFixture(t, true))
	_, err := renderFixture(t, p, dir, map[string]any{
		"path": "template.docx", "output": "out.docx",
		"data": map[string]any{
			"policyNo": "P-1", "title": "Steps",
			"steps": []any{
				map[string]any{"description": "Step one"},
				map[string]any{"description": "Step two"},
				map[string]any{"description": "Step three"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(filepath.Join(dir, "out.docx"))
	if err != nil {
		t.Fatal(err)
	}
	document := readDocxPart(t, out, "word/document.xml")
	table, rows := tableOf(t, document)
	if len(rows) != 5 {
		t.Fatalf("rows = %d, want 5 (2 headers + 3 steps)", len(rows))
	}
	if got := cellText(t, document, rows[0].direct("tc")[1]); got != "P-1" {
		t.Fatalf("policy = %q", got)
	}
	if got := cellText(t, document, rows[1].direct("tc")[0]); got != "Steps" {
		t.Fatalf("title = %q", got)
	}
	for i, want := range []string{"Step one", "Step two", "Step three"} {
		if got := cellText(t, document, rows[2+i].direct("tc")[0]); got != want {
			t.Fatalf("step %d = %q, want %q", i, got, want)
		}
	}
	grid := table.firstDirect("tblGrid")
	if got := xmlAttr(document, grid.direct("gridCol")[0], "w"); got != "2343" {
		t.Fatalf("first grid column = %q", got)
	}
	stepCell := rows[2].direct("tc")[0]
	if got := xmlAttr(document, stepCell.firstDirect("tcPr").firstDirect("tcW"), "w"); got != "1375" {
		t.Fatalf("step cell width = %q", got)
	}
	if !bytes.Contains(document[stepCell.start:stepCell.end], []byte(`<w:spacing w:before="157"/>`)) {
		t.Fatal("step paragraph spacing was not preserved")
	}
	if !bytes.Contains(document[stepCell.start:stepCell.end], []byte(`<w:b/>`)) {
		t.Fatal("step run formatting was not preserved")
	}
}

func TestRenderInsertsImagesAndRelationships(t *testing.T) {
	p, dir := newTestPlugin(t)
	writeFile(t, dir, "template.docx", reportFixture(t, true))
	for _, name := range []string{"a.png", "b.png", "c.png"} {
		writeFile(t, dir, name, tinyPNG(t))
	}
	_, err := renderFixture(t, p, dir, map[string]any{
		"path": "template.docx", "output": "out.docx",
		"images_per_row": 2, "image_width_px": 64,
		"data": map[string]any{
			"policyNo": "P", "title": "T",
			"steps": []any{
				map[string]any{"description": "one", "images": []string{"a.png"}},
				map[string]any{"description": "two", "images": []string{"b.png", "c.png"}},
				map[string]any{"description": "three"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(filepath.Join(dir, "out.docx"))
	if err != nil {
		t.Fatal(err)
	}
	document := readDocxPart(t, out, "word/document.xml")
	if got := strings.Count(string(document), "<w:drawing>"); got != 3 {
		t.Fatalf("drawings = %d, want 3", got)
	}
	if !bytes.Contains(document, []byte(`cx="609600"`)) {
		t.Fatal("image width was not applied")
	}
	rels := readDocxPart(t, out, "word/_rels/document.xml.rels")
	if got := strings.Count(string(rels), "relationships/image"); got != 4 {
		t.Fatalf("image relationships = %d, want 4", got)
	}
	embeds := regexp.MustCompile(`r:embed="([^"]+)"`).FindAllStringSubmatch(string(document), -1)
	seen := map[string]bool{}
	for _, m := range embeds {
		if seen[m[1]] {
			t.Fatalf("duplicate relationship %s", m[1])
		}
		seen[m[1]] = true
	}
	if len(embeds) != 3 {
		t.Fatalf("embeds = %d, want 3", len(embeds))
	}
	members, err := zip.OpenReader(filepath.Join(dir, "out.docx"))
	if err != nil {
		t.Fatal(err)
	}
	defer members.Close()
	media := 0
	for _, f := range members.File {
		if strings.HasPrefix(f.Name, "word/media/") {
			media++
		}
	}
	if media != 4 {
		t.Fatalf("media parts = %d, want 4", media)
	}
}

func TestRenderDedupesRepeatedImage(t *testing.T) {
	p, dir := newTestPlugin(t)
	writeFile(t, dir, "template.docx", reportFixture(t, true))
	writeFile(t, dir, "same.png", tinyPNG(t))
	_, err := renderFixture(t, p, dir, map[string]any{
		"path": "template.docx", "output": "out.docx",
		"data": map[string]any{
			"steps": []any{
				map[string]any{"description": "one", "images": []string{"same.png"}},
				map[string]any{"description": "two", "images": []string{"same.png"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(filepath.Join(dir, "out.docx"))
	rels := readDocxPart(t, out, "word/_rels/document.xml.rels")
	if got := strings.Count(string(rels), "relationships/image"); got != 2 {
		t.Fatalf("image relationships = %d, want 2 (one reused)", got)
	}
}

func TestRenderEscapesTextAndKeepsRunFormatting(t *testing.T) {
	p, dir := newTestPlugin(t)
	writeFile(t, dir, "template.docx", reportFixture(t, true))
	_, err := renderFixture(t, p, dir, map[string]any{
		"path": "template.docx", "output": "out.docx",
		"data": map[string]any{
			"policyNo": "P & P", "title": "T < T",
			"steps": []any{map[string]any{"description": "A & B < C 😀"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(filepath.Join(dir, "out.docx"))
	document := readDocxPart(t, out, "word/document.xml")
	for _, want := range []string{"P &amp; P", "T &lt; T", "A &amp; B &lt; C 😀"} {
		if !bytes.Contains(document, []byte(want)) {
			t.Fatalf("missing escaped text %q", want)
		}
	}
	if bytes.Contains(document, []byte("A & B < C")) {
		t.Fatal("unescaped text reached the XML")
	}
}

func TestRenderEmptyStepsKeepsHeaderRows(t *testing.T) {
	p, dir := newTestPlugin(t)
	writeFile(t, dir, "template.docx", reportFixture(t, true))
	_, err := renderFixture(t, p, dir, map[string]any{
		"path": "template.docx", "output": "out.docx",
		"data": map[string]any{"policyNo": "P", "title": "T", "steps": []any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(filepath.Join(dir, "out.docx"))
	document := readDocxPart(t, out, "word/document.xml")
	_, rows := tableOf(t, document)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if bytes.Contains(document, []byte("<w:drawing>")) {
		t.Fatal("empty steps kept a prototype drawing")
	}
}

func TestRenderRejectsTemplateWithoutImagePlaceholder(t *testing.T) {
	p, dir := newTestPlugin(t)
	writeFile(t, dir, "template.docx", reportFixture(t, false))
	_, err := renderFixture(t, p, dir, map[string]any{
		"path": "template.docx", "output": "out.docx",
		"data": map[string]any{"steps": []any{map[string]any{"description": "one"}}},
	})
	wantCode(t, err, codeInvalidArguments)
	if _, statErr := os.Stat(filepath.Join(dir, "out.docx")); !os.IsNotExist(statErr) {
		t.Fatal("rejected render wrote an output")
	}
}

func TestRenderRejectsImageOutsideWorkspace(t *testing.T) {
	p, dir := newTestPlugin(t)
	writeFile(t, dir, "template.docx", reportFixture(t, true))
	_, err := renderFixture(t, p, dir, map[string]any{
		"path": "template.docx", "output": "out.docx",
		"data": map[string]any{"steps": []any{map[string]any{"description": "one", "images": []string{"../x.png"}}}},
	})
	wantCode(t, err, codePathOutside)
}

func TestRenderIsDeterministic(t *testing.T) {
	p, dir := newTestPlugin(t)
	writeFile(t, dir, "template.docx", reportFixture(t, true))
	writeFile(t, dir, "a.png", tinyPNG(t))
	params := map[string]any{
		"path": "template.docx", "output": "out.docx",
		"data": map[string]any{"steps": []any{map[string]any{"description": "one", "images": []string{"a.png"}}}},
	}
	if _, err := renderFixture(t, p, dir, params); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(filepath.Join(dir, "out.docx"))
	if _, err := renderFixture(t, p, dir, params); err == nil {
		t.Fatal("overwriting without overwrite=true should fail")
	}
	params["overwrite"] = true
	if _, err := renderFixture(t, p, dir, params); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(dir, "out.docx"))
	if !bytes.Equal(first, second) {
		t.Fatal("same input produced different output")
	}
}

func TestRenderImageOrderAndStepOrder(t *testing.T) {
	p, dir := newTestPlugin(t)
	writeFile(t, dir, "template.docx", reportFixture(t, true))
	for _, name := range []string{"a.png", "b.png", "c.png"} {
		writeFile(t, dir, name, tinyPNG(t))
	}
	_, err := renderFixture(t, p, dir, map[string]any{
		"path": "template.docx", "output": "out.docx", "images_per_row": 1,
		"data": map[string]any{"steps": []any{
			map[string]any{"description": "one", "images": []string{"a.png", "b.png"}},
			map[string]any{"description": "two", "images": []string{"c.png"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(filepath.Join(dir, "out.docx"))
	document := readDocxPart(t, out, "word/document.xml")
	_, rows := tableOf(t, document)
	if got := cellText(t, document, rows[2].direct("tc")[0]); got != "one" {
		t.Fatalf("first step = %q", got)
	}
	if got := strings.Count(string(document[rows[2].start:rows[2].end]), "<w:drawing>"); got != 2 {
		t.Fatalf("first step drawings = %d, want 2", got)
	}
	if got := strings.Count(string(document[rows[3].start:rows[3].end]), "<w:drawing>"); got != 1 {
		t.Fatalf("second step drawings = %d, want 1", got)
	}
}
