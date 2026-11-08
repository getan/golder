// Package svgwasm validates and rasterizes static SVG in an isolated WASI
// guest built from the MIT/Apache-2.0 resvg sources.
package svgwasm

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

const (
	maxModuleBytes = 4 << 20
	maxSVGBytes    = 512 << 10
	maxFontBytes   = 32 << 20
	maxOutputBytes = 8 << 20
	maxElements    = 4096
)

var slots = make(chan struct{}, 2)

// Render runs a bounded guest with stdin/stdout only. No filesystem, network,
// environment or inherited process streams are made available to the guest.
func Render(ctx context.Context, module, svg, font []byte) ([]byte, error) {
	if len(module) < 8 || len(module) > maxModuleBytes || string(module[:4]) != "\x00asm" || len(svg) == 0 || len(svg) > maxSVGBytes || len(font) == 0 || len(font) > maxFontBytes {
		return nil, errors.New("invalid SVG renderer input size")
	}
	if err := Validate(svg); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var header [4]byte
	input := bytes.NewBuffer(make([]byte, 0, 8+len(svg)+len(font)))
	binary.LittleEndian.PutUint32(header[:], uint32(len(svg)))
	input.Write(header[:])
	input.Write(svg)
	binary.LittleEndian.PutUint32(header[:], uint32(len(font)))
	input.Write(header[:])
	input.Write(font)
	output := &boundedBuffer{limit: maxOutputBytes}
	config := wazero.NewRuntimeConfigInterpreter().WithMemoryLimitPages(4096).WithCloseOnContextDone(true)
	runtime := wazero.NewRuntimeWithConfig(ctx, config)
	defer runtime.Close(context.Background())
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		return nil, fmt.Errorf("initialize SVG sandbox: %w", err)
	}
	compiled, err := runtime.CompileModule(ctx, module)
	if err != nil {
		return nil, fmt.Errorf("compile SVG renderer: %w", err)
	}
	guest := wazero.NewModuleConfig().WithStdin(input).WithStdout(output).WithStderr(io.Discard)
	if _, err := runtime.InstantiateModule(ctx, compiled, guest); err != nil {
		return nil, fmt.Errorf("render SVG: %w", err)
	}
	if output.exceeded || output.Len() == 0 {
		return nil, errors.New("invalid or oversized SVG output")
	}
	return append([]byte(nil), output.Bytes()...), nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.Len() {
		b.exceeded = true
		return 0, errors.New("output exceeds limit")
	}
	return b.Buffer.Write(data)
}

// Validate accepts static SVG only and rejects external resources, executable
// content and XML entity declarations. Complex unsupported SVG falls back to
// source instead of executing anything or silently loading resources.
func Validate(svg []byte) error {
	if len(svg) == 0 || len(svg) > maxSVGBytes {
		return errors.New("invalid SVG size")
	}
	d := xml.NewDecoder(bytes.NewReader(svg))
	depth, roots, count := 0, 0, 0
	inStyle := false
	ids := make(map[string]bool)
	var refs []string
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("invalid SVG XML")
		}
		switch v := token.(type) {
		case xml.StartElement:
			depth++
			count++
			if inStyle {
				return errors.New("nested SVG stylesheet content")
			}
			if depth > 48 || count > maxElements || len(v.Attr) > 64 || !staticElement(v.Name.Local) || v.Name.Space != "" && v.Name.Space != "http://www.w3.org/2000/svg" {
				return errors.New("unsupported SVG element")
			}
			if depth == 1 {
				roots++
				if roots != 1 || v.Name.Local != "svg" {
					return errors.New("invalid SVG root")
				}
			} else if v.Name.Local == "svg" {
				return errors.New("nested SVG is not supported")
			}
			if v.Name.Local == "style" {
				inStyle = true
			}
			for _, a := range v.Attr {
				name := strings.ToLower(a.Name.Local)
				value := strings.TrimSpace(a.Value)
				if strings.HasPrefix(name, "on") || name == "src" || name == "target" || name == "href" && !localReference(value) {
					return errors.New("unsafe SVG attribute")
				}
				if name == "id" {
					if !localReference("#"+value) || ids[value] {
						return errors.New("invalid or duplicate SVG identifier")
					}
					ids[value] = true
				}
				if name == "href" {
					refs = append(refs, value[1:])
				}
				cssRefs, ok := cssReferences(value)
				if !ok {
					return errors.New("unsafe SVG attribute")
				}
				refs = append(refs, cssRefs...)
			}
		case xml.EndElement:
			if v.Name.Local == "style" {
				inStyle = false
			}
			depth--
		case xml.CharData:
			if inStyle {
				cssRefs, ok := cssReferences(string(v))
				if !ok {
					return errors.New("unsafe SVG stylesheet")
				}
				refs = append(refs, cssRefs...)
			}
		case xml.Directive:
			return errors.New("SVG directives are not allowed")
		case xml.ProcInst:
			if v.Target != "xml" || roots != 0 {
				return errors.New("SVG processing instructions are not allowed")
			}
		}
	}
	if roots != 1 || depth != 0 {
		return errors.New("invalid SVG document")
	}
	for _, id := range refs {
		if !ids[id] {
			return errors.New("unresolved SVG reference")
		}
	}
	return nil
}

func staticElement(name string) bool {
	switch name {
	case "svg", "g", "defs", "path", "rect", "circle", "ellipse", "line", "polyline", "polygon", "text", "tspan", "linearGradient", "radialGradient", "stop", "clipPath", "mask", "filter", "feGaussianBlur", "feOffset", "feBlend", "feColorMatrix", "feComponentTransfer", "feFuncR", "feFuncG", "feFuncB", "feFuncA", "feComposite", "feFlood", "feMerge", "feMergeNode", "feMorphology", "feDropShadow", "marker", "use", "symbol", "style", "title", "desc", "metadata", "pattern":
		return true
	}
	return false
}

func localReference(value string) bool {
	if !strings.HasPrefix(value, "#") || len(value) < 2 || len(value) > 128 {
		return false
	}
	for _, r := range value[1:] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

func cssReferences(value string) ([]string, bool) {
	lower := strings.ToLower(value)
	if strings.ContainsAny(lower, "\\@") || strings.Contains(lower, "javascript:") || strings.Contains(lower, "data:") {
		return nil, false
	}
	var refs []string
	for i := 0; i+4 <= len(value); i++ {
		if !strings.EqualFold(value[i:i+4], "url(") {
			continue
		}
		start := i + 4
		end := strings.IndexByte(value[start:], ')')
		if end < 0 {
			return nil, false
		}
		end += start
		ref := strings.Trim(strings.TrimSpace(value[start:end]), "\"'")
		if !localReference(ref) {
			return nil, false
		}
		refs = append(refs, ref[1:])
		i = end
	}
	return refs, true
}
