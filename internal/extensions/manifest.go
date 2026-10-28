package extensions

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/getan/golder/extension"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type manifest struct {
	Plugin struct {
		ID      string `toml:"id"`
		Version string `toml:"version"`
		Skills  string `toml:"skills"`
	} `toml:"plugin"`
	Tools []cliToolSpec `toml:"tools"`
}
type cliToolSpec struct {
	Name        string   `toml:"name"`
	Description string   `toml:"description"`
	Command     []string `toml:"command"`
	InputSchema string   `toml:"input_schema"`
}

func loadManifest(path string) (manifest, error) {
	var m manifest
	b, e := readLimited(path, 1<<20)
	if e != nil {
		return m, e
	}
	if e = toml.Unmarshal(b, &m); e != nil {
		return m, errors.New("invalid CLI manifest TOML")
	}
	if !idPattern.MatchString(m.Plugin.ID) {
		return m, errors.New("invalid CLI manifest id")
	}
	for _, t := range m.Tools {
		if t.Name == "" || len(t.Command) == 0 || strings.Contains(t.Command[0], "{{") {
			return m, errors.New("CLI tool requires a fixed executable and a name")
		}
	}
	return m, nil
}

// ModelName is deterministic even when multiple wire names sanitize alike.
func ModelName(ref SourceRef, name string) string {
	slug := func(s string) string {
		var b strings.Builder
		for _, r := range s {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
				b.WriteRune(r)
			} else {
				b.WriteByte('_')
			}
		}
		return b.String()
	}
	prefix := "plg"
	if ref.Kind == "mcp" {
		prefix = "mcp"
	}
	out := prefix + "__" + slug(ref.ID) + "__" + slug(name)
	if slug(ref.ID) != ref.ID || slug(name) != name || len(out) > 64 {
		sum := sha256.Sum256([]byte(ref.String() + "\x00" + name))
		if len(out) > 53 {
			out = out[:53]
		}
		out += "__" + hex.EncodeToString(sum[:4])
	}
	return out
}

func compileSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	if len(raw) == 0 || len(raw) > 256<<10 {
		return nil, errors.New("schema missing or exceeds 256 KiB")
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil, errors.New("invalid schema JSON")
	}
	m, ok := v.(map[string]any)
	if !ok || m["type"] != "object" {
		return nil, errors.New("input schema must declare type object")
	}
	var check func(any) error
	check = func(v any) error {
		switch x := v.(type) {
		case map[string]any:
			for k, v := range x {
				if k == "$ref" || k == "$dynamicRef" || k == "$recursiveRef" {
					s, ok := v.(string)
					if !ok || !strings.HasPrefix(s, "#") {
						return errors.New("only local schema references are permitted")
					}
				}
				if k == "$id" {
					return errors.New("schema ids are not permitted in external schemas")
				}
				if e := check(v); e != nil {
					return e
				}
			}
		case []any:
			for _, v := range x {
				if e := check(v); e != nil {
					return e
				}
			}
		}
		return nil
	}
	if e := check(v); e != nil {
		return nil, e
	}
	doc, e := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if e != nil {
		return nil, e
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(noSchemaIO{})
	if e = c.AddResource("mem:///extension.json", doc); e != nil {
		return nil, e
	}
	return c.Compile("mem:///extension.json")
}

type noSchemaIO struct{}

func (noSchemaIO) Load(string) (any, error) {
	return nil, errors.New("external schema loading is forbidden")
}

func renderArgv(t cliToolSpec, args json.RawMessage) ([]string, error) {
	var obj map[string]json.RawMessage
	if e := json.Unmarshal(args, &obj); e != nil {
		return nil, e
	}
	out := append([]string{}, t.Command...)
	for i, v := range out {
		if !strings.Contains(v, "{{") && !strings.Contains(v, "}}") {
			continue
		}
		if i == 0 || !strings.HasPrefix(v, "{{") || !strings.HasSuffix(v, "}}") || strings.Count(v, "{{") != 1 {
			return nil, errors.New("placeholder must occupy one complete argv element")
		}
		key := v[2 : len(v)-2]
		raw, ok := obj[key]
		if !ok {
			return nil, fmt.Errorf("missing argument %q", key)
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			out[i] = s
		} else {
			out[i] = string(raw)
		}
	}
	return out, nil
}

func specOf(t cliToolSpec) extension.ToolSpec {
	return extension.ToolSpec{Name: t.Name, Description: t.Description, InputSchema: json.RawMessage(t.InputSchema)}
}
