package extensions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/getan/golder/extension"
	"github.com/getan/golder/internal/jsonrpc"
	legacy "github.com/getan/golder/internal/plugin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func openBackend(ctx, life context.Context, c SourceConfig, cwd string, logs *logBuffer, dirty func()) (backend, error) {
	if c.Runtime == "cli" {
		mf, e := loadManifest(expandPath(c.Manifest))
		return &cliBackend{mf: mf, cfg: c, cwd: cwd, logs: logs}, e
	}
	if c.Runtime == "rpc" {
		cmd, cleanup, e := prepareCommand(life, c, cwd, append([]string{c.Command}, c.Args...), logs)
		if e != nil {
			return nil, e
		}
		client, e := jsonrpc.NewClient(jsonrpc.Config{Process: cmd})
		if e != nil {
			cleanup()
			return nil, e
		}
		raw, e := client.Call(ctx, "initialize", nil)
		if e != nil {
			_ = client.Close()
			cleanup()
			return nil, e
		}
		var manifest legacy.Manifest
		if json.Unmarshal(raw, &manifest) != nil || manifest.Name == "" {
			_ = client.Close()
			cleanup()
			return nil, errors.New("invalid legacy plugin manifest")
		}
		return &rpcBackend{client: client, manifest: manifest, cleanup: cleanup}, nil
	}
	b := &mcpBackend{progress: map[string]func(extension.Progress){}}
	client := mcp.NewClient(&mcp.Implementation{Name: "golder", Version: "1"}, &mcp.ClientOptions{
		Capabilities:               &mcp.ClientCapabilities{},
		ToolListChangedHandler:     func(context.Context, *mcp.ToolListChangedRequest) { dirty() },
		PromptListChangedHandler:   func(context.Context, *mcp.PromptListChangedRequest) { dirty() },
		ResourceListChangedHandler: func(context.Context, *mcp.ResourceListChangedRequest) { dirty() },
		ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
			b.mu.Lock()
			defer b.mu.Unlock()
			if f := b.progress[fmt.Sprint(r.Params.ProgressToken)]; f != nil {
				p := extension.Progress{Message: clean(r.Params.Message, 1024), Current: r.Params.Progress}
				if r.Params.Total > 0 {
					total := r.Params.Total
					p.Total = &total
				}
				f(p)
			}
		},
	})
	var transport mcp.Transport
	if c.Transport == "http" {
		headers := http.Header{}
		for key, value := range c.Headers {
			v, e := resolveValue(value)
			if e != nil {
				return nil, e
			}
			headers.Set(key, v)
		}
		transport = &mcp.StreamableClientTransport{Endpoint: c.URL, HTTPClient: &http.Client{Transport: &headerTransport{base: http.DefaultTransport, headers: headers}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("MCP redirects are disabled") }}}
	} else {
		cmd, cleanup, e := prepareCommand(life, c, cwd, append([]string{c.Command}, c.Args...), logs)
		if e != nil {
			return nil, e
		}
		b.cleanup = cleanup
		b.command = cmd
		// IOTransport supplies a framing bound; own process cleanup also kills
		// descendants rather than only the direct SDK child.
		in, e := cmd.StdinPipe()
		if e != nil {
			cleanup()
			return nil, e
		}
		out, e := cmd.StdoutPipe()
		if e != nil {
			_ = in.Close()
			cleanup()
			return nil, e
		}
		if e = cmd.Start(); e != nil {
			_ = in.Close()
			_ = out.Close()
			cleanup()
			return nil, e
		}
		b.wait = make(chan struct{})
		go func() { _ = cmd.Wait(); close(b.wait) }()
		transport = &mcp.IOTransport{Reader: out, Writer: in, MaxLineLength: 16 << 20}
	}
	session, e := client.Connect(ctx, transport, nil)
	if e != nil {
		_ = b.Close()
		return nil, e
	}
	b.session = session
	return b, nil
}

type headerTransport struct {
	base    http.RoundTripper
	headers http.Header
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header = req.Header.Clone()
	for k, vs := range t.headers {
		r.Header[k] = append([]string{}, vs...)
	}
	resp, e := t.base.RoundTrip(r)
	if e == nil {
		resp.Body = &limitedBody{ReadCloser: resp.Body, r: io.LimitReader(resp.Body, 16<<20)}
	}
	return resp, e
}

type limitedBody struct {
	io.ReadCloser
	r io.Reader
}

func (b *limitedBody) Read(p []byte) (int, error) { return b.r.Read(p) }

type cliBackend struct {
	mf   manifest
	cfg  SourceConfig
	cwd  string
	logs *logBuffer
}

func (b *cliBackend) Catalog(context.Context) (Catalog, error) {
	cat := Catalog{Protocol: "argv/1", Version: b.mf.Plugin.Version}
	for _, t := range b.mf.Tools {
		cat.Tools = append(cat.Tools, specOf(t))
	}
	return cat, nil
}
func (b *cliBackend) Call(ctx context.Context, c extension.Call, _ func(extension.Progress)) (extension.Result, error) {
	for _, t := range b.mf.Tools {
		if t.Name != c.Tool {
			continue
		}
		argv, e := renderArgv(t, c.Arguments)
		if e != nil {
			return extension.Result{}, e
		}
		cmd, cleanup, e := prepareCommand(ctx, b.cfg, b.cwd, argv, b.logs)
		if e != nil {
			return extension.Result{}, e
		}
		defer cleanup()
		out := &logBuffer{limit: outputLimit}
		cmd.Stdout = out
		e = cmd.Run()
		r := extension.Text(out.String())
		if e != nil {
			r.IsError = true
			r.Content = append(r.Content, extension.ContentBlock{Type: "text", Text: fmt.Sprintf("CLI exited unsuccessfully (%s); output may be partial", clean(e.Error(), 512))})
		}
		return r, nil
	}
	return extension.Result{}, errors.New("unknown CLI tool")
}
func (b *cliBackend) GetPrompt(context.Context, string, map[string]string) (string, error) {
	return "", errors.New("CLI sources do not provide MCP prompts")
}
func (b *cliBackend) ReadResource(context.Context, string) (string, error) {
	return "", errors.New("CLI sources do not provide MCP resources")
}
func (b *cliBackend) Close() error { return nil }

type rpcBackend struct {
	client   *jsonrpc.Client
	manifest legacy.Manifest
	cleanup  func()
}

func (b *rpcBackend) Catalog(context.Context) (Catalog, error) {
	cat := Catalog{Events: b.manifest.Events, Protocol: "golder-plugin/1", Version: b.manifest.Version}
	for _, t := range b.manifest.Tools {
		cat.Tools = append(cat.Tools, extension.ToolSpec{Name: t.Name, Description: t.Description, InputSchema: t.Schema})
	}
	for _, c := range b.manifest.Commands {
		cat.Commands = append(cat.Commands, Item{Name: clean(c.Name, 128), Description: clean(c.Description, 2048)})
	}
	return cat, nil
}
func (b *rpcBackend) Call(ctx context.Context, c extension.Call, _ func(extension.Progress)) (extension.Result, error) {
	raw, e := b.client.Call(ctx, "tools/call", legacy.CallParams{Name: c.Tool, Arguments: c.Arguments})
	if e != nil {
		return extension.Result{}, e
	}
	var r legacy.CallResult
	if e = json.Unmarshal(raw, &r); e != nil {
		return extension.Result{}, e
	}
	out := extension.Text(clean(r.Content, outputLimit))
	out.IsError = r.IsError
	return out, nil
}
func (b *rpcBackend) GetPrompt(context.Context, string, map[string]string) (string, error) {
	return "", errors.New("legacy commands are not MCP prompts; dynamic command execution requires a gated tool")
}
func (b *rpcBackend) ReadResource(context.Context, string) (string, error) {
	return "", errors.New("legacy plugin has no resource API")
}
func (b *rpcBackend) Close() error { e := b.client.Close(); b.cleanup(); return e }

type mcpBackend struct {
	session  *mcp.ClientSession
	command  *exec.Cmd
	cleanup  func()
	wait     chan struct{}
	mu       sync.Mutex
	progress map[string]func(extension.Progress)
}

func (b *mcpBackend) Catalog(ctx context.Context) (Catalog, error) {
	cat := Catalog{Protocol: b.session.InitializeResult().ProtocolVersion}
	if info := b.session.InitializeResult().ServerInfo; info != nil {
		cat.Version = info.Version
	}
	caps := b.session.InitializeResult().Capabilities
	if caps.Tools != nil {
		cursor := ""
		seen := map[string]bool{}
		for page := 0; page < 100; page++ {
			r, e := b.session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
			if e != nil {
				return cat, e
			}
			for _, t := range r.Tools {
				input, _ := json.Marshal(t.InputSchema)
				var output json.RawMessage
				if t.OutputSchema != nil {
					output, _ = json.Marshal(t.OutputSchema)
				}
				spec := extension.ToolSpec{Name: t.Name, Description: clean(t.Description, 4096), InputSchema: input, OutputSchema: output}
				if a := t.Annotations; a != nil {
					spec.Hints = extension.ToolHints{ReadOnly: a.ReadOnlyHint, Idempotent: a.IdempotentHint, Destructive: a.DestructiveHint != nil && *a.DestructiveHint}
				}
				cat.Tools = append(cat.Tools, spec)
			}
			if r.NextCursor == "" {
				break
			}
			if seen[r.NextCursor] {
				return cat, errors.New("MCP repeated pagination cursor")
			}
			seen[r.NextCursor] = true
			cursor = r.NextCursor
			if page == 99 {
				return cat, errors.New("MCP tools directory exceeds page limit")
			}
		}
	}
	if caps.Prompts != nil {
		cursor := ""
		for page := 0; page < 100; page++ {
			r, e := b.session.ListPrompts(ctx, &mcp.ListPromptsParams{Cursor: cursor})
			if e != nil {
				return cat, e
			}
			for _, p := range r.Prompts {
				cat.Prompts = append(cat.Prompts, Item{Name: clean(p.Name, 256), Description: clean(p.Description, 2048)})
			}
			if r.NextCursor == "" {
				break
			}
			if cursor == r.NextCursor {
				return cat, errors.New("MCP repeated pagination cursor")
			}
			cursor = r.NextCursor
		}
	}
	if caps.Resources != nil {
		cursor := ""
		for page := 0; page < 100; page++ {
			r, e := b.session.ListResources(ctx, &mcp.ListResourcesParams{Cursor: cursor})
			if e != nil {
				return cat, e
			}
			for _, r := range r.Resources {
				cat.Resources = append(cat.Resources, Item{Name: clean(r.Name, 256), Description: clean(r.Description, 2048), URI: r.URI})
			}
			if r.NextCursor == "" {
				break
			}
			if cursor == r.NextCursor {
				return cat, errors.New("MCP repeated pagination cursor")
			}
			cursor = r.NextCursor
		}
	}
	return cat, nil
}
func (b *mcpBackend) Call(ctx context.Context, c extension.Call, report func(extension.Progress)) (extension.Result, error) {
	b.mu.Lock()
	b.progress[c.ID] = report
	b.mu.Unlock()
	defer func() { b.mu.Lock(); delete(b.progress, c.ID); b.mu.Unlock() }()
	p := &mcp.CallToolParams{Name: c.Tool, Arguments: c.Arguments}
	if c.OperatorConfirmed {
		p.Meta = mcp.Meta{extension.OperatorConfirmedMeta: true}
	}
	p.SetProgressToken(c.ID)
	r, e := b.session.CallTool(ctx, p)
	if e != nil {
		return extension.Result{}, e
	}
	out := extension.Result{IsError: r.IsError}
	budget := outputLimit
	for _, content := range r.Content {
		if budget <= 0 {
			out.Content = append(out.Content, extension.ContentBlock{Type: "text", Text: "[output truncated]"})
			break
		}
		switch v := content.(type) {
		case *mcp.TextContent:
			s := clean(v.Text, budget)
			budget -= len(s)
			out.Content = append(out.Content, extension.ContentBlock{Type: "text", Text: s})
		case *mcp.ImageContent:
			if len(v.Data) > budget {
				out.Content = append(out.Content, extension.ContentBlock{Type: "text", Text: "[image exceeds output budget]"})
			} else {
				budget -= len(v.Data)
				out.Content = append(out.Content, extension.ContentBlock{Type: "image", Data: base64.StdEncoding.EncodeToString(v.Data), MIMEType: v.MIMEType})
			}
		case *mcp.ResourceLink:
			out.Content = append(out.Content, extension.ContentBlock{Type: "resource_link", URI: clean(v.URI, 2048), Text: clean(v.Name, 256)})
		default:
			raw, _ := json.Marshal(content)
			if len(raw) > budget {
				out.Content = append(out.Content, extension.ContentBlock{Type: "text", Text: "[unsupported content omitted]"})
			} else {
				s := clean(string(raw), budget)
				budget -= len(s)
				out.Content = append(out.Content, extension.ContentBlock{Type: "text", Text: s})
			}
		}
	}
	if r.StructuredContent != nil {
		raw, e := json.Marshal(r.StructuredContent)
		if e == nil && len(raw) <= budget {
			out.StructuredContent = raw
		} else {
			out.Content = append(out.Content, extension.ContentBlock{Type: "text", Text: "[structured content exceeds output budget]"})
		}
	}
	return out, nil
}
func (b *mcpBackend) GetPrompt(ctx context.Context, name string, args map[string]string) (string, error) {
	r, e := b.session.GetPrompt(ctx, &mcp.GetPromptParams{Name: name, Arguments: args})
	if e != nil {
		return "", e
	}
	var parts []string
	for _, m := range r.Messages {
		if t, ok := m.Content.(*mcp.TextContent); ok {
			parts = append(parts, string(m.Role)+": "+t.Text)
		} else {
			parts = append(parts, "[non-text prompt content]")
		}
	}
	return clean(strings.Join(parts, "\n"), outputLimit), nil
}
func (b *mcpBackend) ReadResource(ctx context.Context, uri string) (string, error) {
	r, e := b.session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if e != nil {
		return "", e
	}
	var parts []string
	for _, c := range r.Contents {
		parts = append(parts, c.URI+"\n"+c.Text)
		if len(c.Blob) > 0 {
			parts = append(parts, "[binary resource omitted]")
		}
	}
	return clean(strings.Join(parts, "\n"), outputLimit), nil
}
func (b *mcpBackend) Close() error {
	var e error
	if b.cleanup != nil {
		defer b.cleanup()
	}
	if b.session != nil {
		e = b.session.Close()
	}
	if b.command != nil {
		killProcess(b.command)
		if b.wait != nil {
			select {
			case <-b.wait:
			case <-time.After(2 * time.Second):
			}
		}
	}
	return e
}
