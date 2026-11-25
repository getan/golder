package docxmcp

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strings"
)

var oleHeader = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

type inspected struct {
	data  []byte
	files map[string]*zip.File
}

func (p *inspected) file(name string) (*zip.File, bool) {
	f, ok := p.files[name]
	return f, ok
}

// inspectDocx validates the container before docxgo ever sees it: zip limits,
// safe part names, no encryption, no macro/active content, bounded XML shape.
func inspectDocx(data []byte) (*inspected, error) {
	if len(data) > maxInputBytes {
		return nil, errf(codeFileTooLarge, "document exceeds the %d MiB input limit", maxInputBytes>>20)
	}
	if len(data) >= len(oleHeader) && bytes.Equal(data[:len(oleHeader)], oleHeader) {
		return nil, errf(codeEncryptedDocx, "OLE/encrypted packages are not accepted")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errf(codeInvalidDocx, "not a valid DOCX zip package")
	}
	if len(zr.File) > maxZipEntries {
		return nil, errf(codeZipLimit, "package has more than %d entries", maxZipEntries)
	}
	files := make(map[string]*zip.File, len(zr.File))
	seen := make(map[string]string, len(zr.File))
	var total uint64
	var contentTypes []byte
	mainSeen := false
	for _, f := range zr.File {
		if f == nil {
			continue
		}
		name := f.Name
		if err := validatePartName(name); err != nil {
			return nil, err
		}
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, "/") {
			continue
		}
		if prev, ok := seen[lower]; ok {
			return nil, errf(codeInvalidDocx, "duplicate part name %q conflicts with %q", name, prev)
		}
		seen[lower] = name
		if f.Flags&1 != 0 {
			return nil, errf(codeEncryptedDocx, "encrypted zip entries are not accepted")
		}
		if f.UncompressedSize64 > maxEntryBytes {
			return nil, errf(codeZipLimit, "part %q exceeds the %d MiB entry limit", name, maxEntryBytes>>20)
		}
		if f.CompressedSize64 > 0 && f.UncompressedSize64 > f.CompressedSize64*maxCompressionRate {
			return nil, errf(codeZipLimit, "part %q exceeds the compression-ratio limit", name)
		}
		if total > maxTotalBytes-f.UncompressedSize64 {
			return nil, errf(codeZipLimit, "package exceeds the %d MiB extracted limit", maxTotalBytes>>20)
		}
		total += f.UncompressedSize64
		if isActivePart(lower) {
			if strings.Contains(lower, "vba") {
				return nil, errf(codeMacroEnabled, "macro-enabled content is not accepted")
			}
			return nil, errf(codeEmbeddedObject, "embedded or active content is not accepted")
		}
		switch lower {
		case "word/document.xml":
			mainSeen = true
		case "[content_types].xml":
			if contentTypes == nil {
				contentTypes, err = readZipLimited(f, maxXMLBytes)
				if err != nil {
					return nil, err
				}
			}
		}
		files[name] = f
	}
	if !mainSeen {
		return nil, errf(codeInvalidDocx, "word/document.xml is missing")
	}
	if contentTypes == nil {
		return nil, errf(codeInvalidDocx, "[Content_Types].xml is missing")
	}
	if err := checkContentTypes(contentTypes); err != nil {
		return nil, err
	}
	for name, f := range files {
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".xml") && !strings.HasSuffix(lower, ".rels") {
			continue
		}
		part, err := readZipLimited(f, maxXMLBytes)
		if err != nil {
			return nil, err
		}
		if err := scanXML(part); err != nil {
			return nil, err
		}
	}
	return &inspected{data: data, files: files}, nil
}

func validatePartName(name string) error {
	if name == "" || strings.ContainsRune(name, 0) || strings.Contains(name, `\`) {
		return errf(codeInvalidDocx, "unsafe part name")
	}
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") {
		return errf(codeInvalidDocx, "unsafe part name")
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." || seg == "." {
			return errf(codeInvalidDocx, "unsafe part name")
		}
	}
	return nil
}

func isActivePart(lower string) bool {
	for _, marker := range []string{
		"vbaproject", "vbadata", "activex", "oleobject", "embeddings/", "altchunk",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func readZipLimited(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, errf(codeInvalidDocx, "cannot open part %q", f.Name)
	}
	defer rc.Close()
	var b bytes.Buffer
	n, err := io.Copy(&b, io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, errf(codeInvalidDocx, "cannot read part %q", f.Name)
	}
	if n > limit {
		return nil, errf(codeXMLLimit, "part %q exceeds the XML limit", f.Name)
	}
	return b.Bytes(), nil
}

type contentTypes struct {
	XMLName  xml.Name `xml:"Types"`
	Defaults []struct {
		Extension   string `xml:"Extension,attr"`
		ContentType string `xml:"ContentType,attr"`
	} `xml:"Default"`
	Overrides []struct {
		PartName    string `xml:"PartName,attr"`
		ContentType string `xml:"ContentType,attr"`
	} `xml:"Override"`
}

func checkContentTypes(data []byte) error {
	var ct contentTypes
	if err := xml.Unmarshal(data, &ct); err != nil {
		return errf(codeInvalidDocx, "invalid [Content_Types].xml")
	}
	mainOK := false
	for _, o := range ct.Overrides {
		lowerType := strings.ToLower(o.ContentType)
		lowerPart := strings.ToLower(o.PartName)
		for _, bad := range []string{"macroenabled", "vbaproject", "oleobject", "altchunk", "activex"} {
			if strings.Contains(lowerType, bad) {
				return errf(codeMacroEnabled, "macro-enabled or active content is not accepted")
			}
		}
		if lowerPart == "/word/document.xml" && strings.HasSuffix(lowerType, "wordprocessingml.document.main+xml") {
			mainOK = true
		}
	}
	for _, d := range ct.Defaults {
		lowerType := strings.ToLower(d.ContentType)
		for _, bad := range []string{"macroenabled", "vbaproject", "oleobject", "altchunk", "activex"} {
			if strings.Contains(lowerType, bad) {
				return errf(codeMacroEnabled, "macro-enabled or active content is not accepted")
			}
		}
	}
	if !mainOK {
		return errf(codeInvalidDocx, "package is not a standard .docx (macro-enabled packages are rejected)")
	}
	return nil
}

func scanXML(data []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return errf(codeInvalidDocx, "malformed XML part")
		}
		switch tok.(type) {
		case xml.Directive:
			return errf(codeInvalidDocx, "XML directives are not accepted")
		case xml.StartElement:
			depth++
			if depth > maxXMLDepth {
				return errf(codeXMLLimit, "XML nesting exceeds %d levels", maxXMLDepth)
			}
		case xml.EndElement:
			if depth > 0 {
				depth--
			}
		}
	}
}
