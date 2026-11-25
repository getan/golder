package docxmcp

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestPlugin(t *testing.T) (*Plugin, string) {
	t.Helper()
	dir := t.TempDir()
	p, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p, dir
}

func writeFile(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 40, B: 40, A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return b.Bytes()
}

func fixture(t *testing.T, contentType string, mutate func(*zip.Writer)) []byte {
	t.Helper()
	if contentType == "" {
		contentType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) {
		t.Helper()
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
		`<Override PartName="/word/document.xml" ContentType="`+contentType+`"/>`+
		`</Types>`)
	add("_rels/.rels", `<?xml version="1.0" encoding="UTF-8"?>`+
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`+
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>`+
		`</Relationships>`)
	add("word/document.xml", `<?xml version="1.0" encoding="UTF-8"?>`+
		`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">`+
		`<w:body><w:p><w:r><w:t>fixture</w:t></w:r></w:p></w:body></w:document>`)
	if mutate != nil {
		mutate(zw)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var de *Error
	if !errors.As(err, &de) {
		t.Fatalf("error = %v, want *Error", err)
	}
	if de.Code != code {
		t.Fatalf("error code = %s (%s), want %s", de.Code, de.Message, code)
	}
}

func TestCreateReadEditWithImage(t *testing.T) {
	p, dir := newTestPlugin(t)
	writeFile(t, dir, "logo.png", tinyPNG(t))

	createRaw, err := json.Marshal(map[string]any{
		"path":  "report.docx",
		"title": "Quarterly",
		"blocks": []any{
			map[string]any{"type": "heading", "level": 1, "text": "Summary"},
			map[string]any{"type": "paragraph", "text": "Alice approved."},
			map[string]any{"type": "bullets", "items": []string{"one", "two"}},
			map[string]any{"type": "table", "rows": [][]string{{"Name", "Value"}, {"A", "1"}}},
			map[string]any{"type": "image", "path": "logo.png", "width_px": 32, "height_px": 32},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := p.create(context.Background(), createRaw)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if result, ok := created.(writeResult); !ok || result.Images != 1 {
		t.Fatalf("create result = %#v", created)
	}

	readRaw, _ := json.Marshal(map[string]any{"path": "report.docx", "format": "markdown"})
	readOut, err := p.read(context.Background(), readRaw)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	read := readOut.(readResult)
	for _, want := range []string{"# Summary", "Alice approved.", "one", "two", "Name", "Value", "A", "1"} {
		if !strings.Contains(read.Text, want) {
			t.Errorf("read text missing %q:\n%s", want, read.Text)
		}
	}
	if read.ImageCount != 1 {
		t.Errorf("image count = %d, want 1", read.ImageCount)
	}

	outlineRaw, _ := json.Marshal(map[string]any{"path": "report.docx", "format": "outline"})
	outlineOut, err := p.read(context.Background(), outlineRaw)
	if err != nil {
		t.Fatalf("outline: %v", err)
	}
	outline := outlineOut.(readResult)
	if !strings.Contains(outline.Text, "Summary") {
		t.Errorf("outline missing the heading (style not applied?): %q", outline.Text)
	}

	editRaw, _ := json.Marshal(map[string]any{
		"path":         "report.docx",
		"output":       "filled.docx",
		"replacements": map[string]string{"Alice": "Bob"},
		"append":       []any{map[string]any{"type": "paragraph", "text": "Appendix"}},
	})
	edited, err := p.edit(context.Background(), editRaw)
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if result := edited.(editResult); result.Replaced != 1 || result.Appended != 1 {
		t.Errorf("edit result = %+v, want replaced=1 appended=1", result)
	}

	filledRaw, _ := json.Marshal(map[string]any{"path": "filled.docx"})
	filledOut, err := p.read(context.Background(), filledRaw)
	if err != nil {
		t.Fatalf("read filled: %v", err)
	}
	filled := filledOut.(readResult)
	if !strings.Contains(filled.Text, "Bob") || !strings.Contains(filled.Text, "Appendix") {
		t.Errorf("edited document missing replacement or append:\n%s", filled.Text)
	}
	if strings.Contains(filled.Text, "Alice") {
		t.Errorf("edited document still contains the replaced text:\n%s", filled.Text)
	}

	originalOut, err := p.read(context.Background(), readRaw)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}
	if !strings.Contains(originalOut.(readResult).Text, "Alice") {
		t.Error("original input was modified by edit")
	}
}

func TestCreateOverwriteGuard(t *testing.T) {
	p, _ := newTestPlugin(t)
	payload := func(overwrite bool) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{
			"path":      "a.docx",
			"overwrite": overwrite,
			"blocks":    []any{map[string]any{"type": "paragraph", "text": "hello"}},
		})
		return raw
	}
	if _, err := p.create(context.Background(), payload(false)); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := p.create(context.Background(), payload(false))
	wantCode(t, err, codeOutputExists)
	if _, err := p.create(context.Background(), payload(true)); err != nil {
		t.Fatalf("overwrite create: %v", err)
	}
}

func TestWorkspaceRejectsTraversalAndSymlink(t *testing.T) {
	p, dir := newTestPlugin(t)
	outside := filepath.Join(t.TempDir(), "outside.docx")
	if err := os.WriteFile(outside, fixture(t, "", nil), 0o600); err != nil {
		t.Fatal(err)
	}
	traversal, _ := json.Marshal(map[string]any{"path": "../outside.docx"})
	_, err := p.read(context.Background(), traversal)
	wantCode(t, err, codePathOutside)

	absolute, _ := json.Marshal(map[string]any{"path": outside})
	_, err = p.read(context.Background(), absolute)
	wantCode(t, err, codePathOutside)

	if err := os.Symlink(outside, filepath.Join(dir, "link.docx")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	link, _ := json.Marshal(map[string]any{"path": "link.docx"})
	_, err = p.read(context.Background(), link)
	wantCode(t, err, codeSymlinkRejected)
}

func TestPreflightRejectsActiveContent(t *testing.T) {
	t.Run("vba part", func(t *testing.T) {
		_, err := inspectDocx(fixture(t, "", func(zw *zip.Writer) {
			w, err := zw.Create("word/vbaProject.bin")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte("macro"))
		}))
		wantCode(t, err, codeMacroEnabled)
	})
	t.Run("macro content type", func(t *testing.T) {
		_, err := inspectDocx(fixture(t, "application/vnd.ms-word.document.macroEnabled.main+xml", nil))
		wantCode(t, err, codeMacroEnabled)
	})
	t.Run("ole package", func(t *testing.T) {
		_, err := inspectDocx(append(append([]byte{}, oleHeader...), make([]byte, 64)...))
		wantCode(t, err, codeEncryptedDocx)
	})
	t.Run("docm extension", func(t *testing.T) {
		p, dir := newTestPlugin(t)
		writeFile(t, dir, "legacy.docm", fixture(t, "application/vnd.ms-word.document.macroEnabled.main+xml", nil))
		raw, _ := json.Marshal(map[string]any{"path": "legacy.docm"})
		_, err := p.read(context.Background(), raw)
		wantCode(t, err, codeUnsupportedExt)
	})
}

func TestPreflightRejectsDeepXMLAndUnsafeNames(t *testing.T) {
	t.Run("deep xml", func(t *testing.T) {
		var deep strings.Builder
		deep.WriteString(`<?xml version="1.0"?><root>`)
		for i := 0; i < maxXMLDepth+10; i++ {
			deep.WriteString("<x>")
		}
		for i := 0; i < maxXMLDepth+10; i++ {
			deep.WriteString("</x>")
		}
		deep.WriteString(`</root>`)
		_, err := inspectDocx(fixture(t, "", func(zw *zip.Writer) {
			w, err := zw.Create("word/deep.xml")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(deep.String()))
		}))
		wantCode(t, err, codeXMLLimit)
	})
	t.Run("unsafe part name", func(t *testing.T) {
		_, err := inspectDocx(fixture(t, "", func(zw *zip.Writer) {
			w, err := zw.Create("word/../evil.xml")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte("<x/>"))
		}))
		wantCode(t, err, codeInvalidDocx)
	})
	t.Run("duplicate part name", func(t *testing.T) {
		_, err := inspectDocx(fixture(t, "", func(zw *zip.Writer) {
			w, err := zw.Create("WORD/DOCUMENT.XML")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte("<x/>"))
		}))
		wantCode(t, err, codeInvalidDocx)
	})
}

func TestDecodeArgsRejectsUnknown(t *testing.T) {
	p, _ := newTestPlugin(t)
	raw := json.RawMessage(`{"path":"x.docx","bogus":true}`)
	_, err := p.read(context.Background(), raw)
	wantCode(t, err, codeInvalidArguments)
}

func TestCreateRejectsInvalidBlocks(t *testing.T) {
	p, _ := newTestPlugin(t)
	cases := []map[string]any{
		{"type": "heading", "level": 9, "text": "x"},
		{"type": "paragraph"},
		{"type": "table", "rows": [][]string{{"a", "b"}, {"c"}}},
		{"type": "image"},
		{"type": "unknown"},
	}
	for _, block := range cases {
		raw, _ := json.Marshal(map[string]any{
			"path":   "bad.docx",
			"blocks": []any{block},
		})
		_, err := p.create(context.Background(), raw)
		if err == nil {
			t.Errorf("block %#v was accepted", block)
		}
	}
}

func TestPreflightRejectsTooManyEntries(t *testing.T) {
	_, err := inspectDocx(fixture(t, "", func(zw *zip.Writer) {
		for i := 0; i < maxZipEntries; i++ {
			w, err := zw.Create("word/extra-" + strings.Repeat("x", 8) + string(rune('a'+i%26)) + ".xml")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte("<x/>"))
		}
	}))
	wantCode(t, err, codeZipLimit)
}

func TestReadTruncatesAtRequestedLimit(t *testing.T) {
	p, _ := newTestPlugin(t)
	createRaw, _ := json.Marshal(map[string]any{
		"path":   "long.docx",
		"blocks": []any{map[string]any{"type": "paragraph", "text": strings.Repeat("abcdefghij", 20)}},
	})
	if _, err := p.create(context.Background(), createRaw); err != nil {
		t.Fatalf("create: %v", err)
	}
	readRaw, _ := json.Marshal(map[string]any{"path": "long.docx", "max_chars": 10})
	out, err := p.read(context.Background(), readRaw)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	result := out.(readResult)
	if !result.Truncated || len([]rune(result.Text)) != 10 {
		t.Fatalf("truncated=%v chars=%d, want true/10", result.Truncated, len([]rune(result.Text)))
	}
}

func TestCreateRejectsOutputOutsideWorkspace(t *testing.T) {
	p, _ := newTestPlugin(t)
	raw, _ := json.Marshal(map[string]any{
		"path":   "../escape.docx",
		"blocks": []any{map[string]any{"type": "paragraph", "text": "x"}},
	})
	_, err := p.create(context.Background(), raw)
	wantCode(t, err, codePathOutside)
}
