// Package extensioncmd is the shared text command surface for the CLI, REPL and
// TUI. It is not registered as a model tool. Connecting is an operator action.
package extensioncmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/getan/golder/internal/extensions"
	"github.com/getan/golder/internal/runtime"
	"github.com/spf13/pflag"
)

type Response struct {
	Text   string
	Prompt string
}

func RenderList(m *extensions.Manager, kind string) string {
	var b strings.Builder
	if kind != "skills" {
		b.WriteString("Tool sources\n")
		for _, s := range m.List() {
			if kind == "mcp" && s.Ref.Kind != "mcp" || kind == "plugin" && s.Ref.Kind != "plugin" {
				continue
			}
			n := 0
			for _, t := range s.Tools {
				if t.State == "available" {
					n++
				}
			}
			fmt.Fprintf(&b, "  %s  [%s]  %s · %s · %d/%d tools\n", s.Ref, s.Type, s.State, s.Trust, n, len(s.Tools))
		}
	}
	if kind == "extensions" || kind == "skills" {
		b.WriteString("Skills\n")
		for _, s := range m.SkillViews() {
			owner := "user"
			if s.ProvidedBy != "" {
				owner = "provided by " + s.ProvidedBy
			}
			fmt.Fprintf(&b, "  %s  [Skill]  %s · %s\n", s.Name, s.State, owner)
		}
	}
	return b.String()
}
func Detail(m *extensions.Manager, ref extensions.SourceRef) (string, error) {
	for _, s := range m.List() {
		if s.Ref == ref {
			b, _ := json.MarshalIndent(s, "", "  ")
			return string(b), nil
		}
	}
	return "", errors.New("extension not found")
}

func Execute(ctx context.Context, m *extensions.Manager, kind string, args []string) (Response, error) {
	if m == nil {
		return Response{}, errors.New("extension manager unavailable")
	}
	if len(args) == 0 || args[0] == "list" || args[0] == "doctor" {
		return Response{Text: RenderList(m, kind)}, nil
	}
	op := args[0]
	if len(args) < 2 {
		return Response{}, errors.New("usage: /" + kind + " <inspect|tools|logs|enable|disable|connect|disconnect|refresh|prompts|resources> <id>")
	}
	id := args[1]
	if kind == "skills" {
		if op == "enable" || op == "disable" {
			if !m.SetSkillEnabled(id, op == "enable") {
				return Response{}, errors.New("skill not found")
			}
			return Response{Text: "Skill " + id + " " + op + "d for this session"}, nil
		}
		s := m.Skill(id)
		if s == nil {
			return Response{}, errors.New("skill unavailable")
		}
		switch op {
		case "inspect":
			return Response{Text: s.Frontmatter.Name + " [Skill]\n" + s.Path + "\n" + s.Frontmatter.Description}, nil
		case "preview":
			return Response{Text: s.Body}, nil
		case "use":
			return Response{Text: "Using skill · " + s.Frontmatter.Name, Prompt: s.SlashCommand().Expand(strings.Join(args[2:], " "))}, nil
		}
		return Response{}, errors.New("unknown skill action")
	}
	ref := extensions.SourceRef{Kind: kind, ID: id}
	if kind == "extensions" {
		k, v, ok := strings.Cut(id, ":")
		if !ok {
			return Response{}, errors.New("use plugin:<id> or mcp:<id>")
		}
		ref = extensions.SourceRef{Kind: k, ID: v}
	}
	if op == "inspect" {
		s, e := Detail(m, ref)
		return Response{Text: s}, e
	}
	if op == "logs" {
		s, e := m.Logs(ref)
		return Response{Text: s}, e
	}
	if op == "tools" || op == "prompts" || op == "resources" {
		for _, s := range m.List() {
			if s.Ref != ref {
				continue
			}
			var b []byte
			switch op {
			case "tools":
				b, _ = json.MarshalIndent(s.Tools, "", "  ")
			case "prompts":
				b, _ = json.MarshalIndent(s.Prompts, "", "  ")
			case "resources":
				b, _ = json.MarshalIndent(s.Resources, "", "  ")
			}
			return Response{Text: string(b)}, nil
		}
		return Response{}, errors.New("extension not found")
	}
	var err error
	switch op {
	case "setup":
		variant := ""
		if len(args) > 2 {
			variant = args[2]
		}
		if len(args) > 3 {
			return Response{}, errors.New("setup accepts only a declared variant name, not executable arguments")
		}
		err = runSetup(ctx, m, ref, variant)
	case "reset":
		if !contains(args[2:], "--yes") {
			return Response{}, errors.New("reset deletes this source's local login state; confirm with reset <id> --yes")
		}
		err = m.Reset(ref)
	case "enable":
		if !contains(args[2:], "--yes") {
			return Response{}, errors.New("enable requires explicit approval: inspect the source, then enable <id> --yes")
		}
		if contains(args[2:], "--save") {
			err = m.Approve(ref)
		} else {
			err = m.ApproveSession(ref)
		}
		if err == nil {
			err = m.Enable(ref)
		}
	case "disable":
		err = m.Disable(ref)
	case "connect", "test":
		err = m.Connect(ctx, ref)
	case "disconnect":
		err = m.Disconnect(ref)
	case "refresh":
		err = m.Refresh(ctx, ref)
	case "reconnect":
		if err = m.Disconnect(ref); err == nil {
			err = m.Connect(ctx, ref)
		}
	case "prompt":
		if len(args) < 3 {
			return Response{}, errors.New("prompt requires a name and optional JSON string arguments")
		}
		values := map[string]string{}
		if len(args) > 3 {
			if e := json.Unmarshal([]byte(strings.Join(args[3:], " ")), &values); e != nil {
				return Response{}, errors.New("prompt arguments must be a JSON object of strings")
			}
		}
		s, e := m.Prompt(ctx, ref, args[2], values)
		return Response{Text: s}, e
	case "resource":
		if len(args) != 3 {
			return Response{}, errors.New("resource requires a URI")
		}
		s, e := m.Resource(ctx, ref, args[2])
		return Response{Text: s}, e
	default:
		return Response{}, errors.New("unknown extension action")
	}
	if err != nil {
		return Response{}, err
	}
	if contains(args[2:], "--save") && (op == "enable" || op == "disable") {
		c, e := m.Config(ref)
		if e != nil {
			return Response{}, e
		}
		c.Enabled = op == "enable"
		if e = extensions.WriteSource(extensions.ConfigPath(), ref, &c); e != nil {
			return Response{}, e
		}
	}
	return Response{Text: ref.String() + ": " + op + " completed"}, nil
}

func contains(args []string, v string) bool {
	for _, a := range args {
		if a == v {
			return true
		}
	}
	return false
}

// Run handles standalone administration. --yes is explicit operator consent,
// not a model-controlled permission. No commands here call business tools.
func Run(kind string, args []string, in io.Reader, out, errOut io.Writer) int {
	jsonOutput := contains(args, "--json")
	fail := func(err error) int {
		if jsonOutput {
			_ = json.NewEncoder(errOut).Encode(map[string]any{"error": map[string]string{"code": "extension_error", "message": err.Error()}})
		} else {
			fmt.Fprintln(errOut, "golder:", err)
		}
		return 1
	}
	fs := pflag.NewFlagSet(kind, pflag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {}
	var yes, j bool
	var transport, url, id string
	fs.BoolVar(&yes, "yes", false, "Approve this exact source and launch ceiling")
	fs.BoolVar(&j, "json", false, "JSON output")
	fs.StringVar(&transport, "transport", "stdio", "stdio or http")
	fs.StringVar(&url, "url", "", "MCP endpoint")
	fs.StringVar(&id, "id", "", "Plugin identifier")
	if e := fs.Parse(args); e != nil {
		if errors.Is(e, pflag.ErrHelp) {
			fmt.Fprintf(out, "Usage: golder %s <action> [id] [flags]\n", kind)
			if kind == "extensions" {
				fmt.Fprintln(out, "  list | doctor           data-only source and Skills overview")
			} else {
				fmt.Fprintln(out, "  list | inspect | tools | logs | prompts | resources")
				fmt.Fprintln(out, "  enable | disable | remove | test | reset <id>")
				fmt.Fprintln(out, "  setup <id> [variant]     explicit user authorization; private browser QR or terminal fallback")
				if kind == "mcp" {
					fmt.Fprintln(out, "  add <id> --transport stdio -- <program> [args...]")
					fmt.Fprintln(out, "  add <id> --transport http --url <url>")
				} else {
					fmt.Fprintln(out, "  add <manifest-or-executable> [--id <id>]")
				}
			}
			fmt.Fprintln(out, "\nRegistration is disabled by default. test starts third-party code/connection, but never invokes a business tool.")
			fs.SetOutput(out)
			fs.PrintDefaults()
			return 0
		}
		return 2
	}
	pos := fs.Args()
	if len(pos) == 0 {
		pos = []string{"list"}
	}
	op := pos[0]
	if op == "add" {
		if len(pos) < 2 {
			return fail(errors.New("add requires a source id or plugin path"))
		}
		ref := extensions.SourceRef{Kind: kind, ID: pos[1]}
		c := extensions.SourceConfig{}
		if kind == "mcp" {
			c.Transport = transport
			c.URL = url
			if transport == "stdio" {
				if len(pos) < 3 {
					return fail(errors.New("stdio requires -- <program> [args...]"))
				}
				c.Command = pos[2]
				c.Args = pos[3:]
			}
		} else if kind == "plugin" {
			p, e := filepath.Abs(pos[1])
			if e != nil {
				return fail(e)
			}
			ref.ID = id
			if ref.ID == "" {
				ref.ID = strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
			}
			if strings.HasSuffix(p, ".toml") {
				c.Runtime = "cli"
				c.Manifest = p
				if id == "" {
					ref.ID = filepath.Base(filepath.Dir(p))
				}
			} else {
				c.Runtime = "rpc"
				c.Command = p
			}
		} else {
			return fail(errors.New("use plugin add or mcp add"))
		}
		cfg, e := extensions.LoadConfig(extensions.ConfigPath())
		if e != nil {
			return fail(e)
		}
		_, exists := cfg.Plugins[ref.ID]
		if kind == "mcp" {
			_, exists = cfg.MCP.Servers[ref.ID]
		}
		if exists {
			return fail(errors.New("source already configured"))
		}
		if e = extensions.WriteSource(extensions.ConfigPath(), ref, &c); e != nil {
			return fail(e)
		}
		if j {
			_ = json.NewEncoder(out).Encode(map[string]any{"source": ref, "enabled": false})
		} else {
			fmt.Fprintln(out, "Registered", ref, "(disabled; no code executed)")
		}
		return 0
	}
	m, e := extensions.New(extensions.Options{})
	if e != nil {
		return fail(e)
	}
	defer m.Close()
	if kind == "extensions" {
		dir := os.Getenv("GOLDER_SKILLS_DIR")
		if dir == "" {
			if home, err := os.UserHomeDir(); err == nil {
				dir = filepath.Join(home, ".agents", "skills")
			}
		}
		skills, _ := runtime.LoadSkillsDir(dir)
		m.SetSkills(skills, false)
	}
	if op == "list" || op == "doctor" {
		if j {
			if kind == "extensions" {
				_ = json.NewEncoder(out).Encode(map[string]any{"sources": m.List(), "skills": m.SkillViews()})
			} else {
				var filtered []extensions.Snapshot
				for _, s := range m.List() {
					if s.Ref.Kind == kind {
						filtered = append(filtered, s)
					}
				}
				_ = json.NewEncoder(out).Encode(filtered)
			}
		} else {
			fmt.Fprint(out, RenderList(m, kind))
		}
		return 0
	}
	if len(pos) < 2 {
		return fail(errors.New("action requires a source id"))
	}
	ref := extensions.SourceRef{Kind: kind, ID: pos[1]}
	if op == "enable" || op == "disable" || op == "remove" {
		c, e := m.Config(ref)
		if e != nil {
			return fail(e)
		}
		if op == "enable" {
			if !yes {
				fmt.Fprintln(errOut, "Approve", ref, "? This permits third-party code/connection inside its configured launch ceiling.")
				fmt.Fprintln(errOut, "Run inspect first; type yes to approve:")
				line, _ := bufio.NewReader(in).ReadString('\n')
				if strings.TrimSpace(line) != "yes" {
					return fail(errors.New("not approved"))
				}
			}
			if e = m.Approve(ref); e != nil {
				return fail(e)
			}
			c.Enabled = true
		} else {
			c.Enabled = false
		}
		var next *extensions.SourceConfig = &c
		if op == "remove" {
			next = nil
		}
		if e = extensions.WriteSource(extensions.ConfigPath(), ref, next); e != nil {
			return fail(e)
		}
		if j {
			_ = json.NewEncoder(out).Encode(map[string]any{"source": ref, "action": op})
		} else {
			fmt.Fprintln(out, ref, op+" saved; applies to future sessions")
		}
		return 0
	}
	if op == "inspect" || op == "tools" || op == "prompts" || op == "resources" {
		r, e := Execute(context.Background(), m, kind, pos)
		if e != nil {
			return fail(e)
		}
		fmt.Fprintln(out, r.Text)
		return 0
	}
	duration := 30 * time.Second
	if op == "setup" {
		duration = 10 * time.Minute
	}
	if op == "reset" {
		if !yes {
			fmt.Fprintln(errOut, "Delete this source's local login state (not server-side consent)? Type yes to confirm:")
			line, _ := bufio.NewReader(in).ReadString('\n')
			if strings.TrimSpace(line) != "yes" {
				return fail(errors.New("reset not confirmed"))
			}
		}
		pos = append(pos, "--yes")
	}
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithTimeout(signalCtx, duration)
	defer cancel()
	ctx = WithSetupDisplay(ctx, func(text string) { fmt.Fprintln(errOut, text) })
	r, e := Execute(ctx, m, kind, pos)
	if e != nil {
		return fail(e)
	}
	if j {
		_ = json.NewEncoder(out).Encode(map[string]string{"status": r.Text})
	} else {
		fmt.Fprintln(out, r.Text)
	}
	return 0
}
