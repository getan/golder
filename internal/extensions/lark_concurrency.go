package extensions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/getan/golder/extension"
)

const larkParallelLimit = 4

// Setup may own source.mu for an operator authorization flow. A business call
// waiting on that ownership must still honor its own cancellation budget.
func lockLarkSource(ctx context.Context, s *source) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !s.enabled.Load() {
			return errors.New("extension disabled; not executed")
		}
		if s.mu.TryLock() {
			return nil
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Native payload digests, extracted only after verifying the pinned v1.0.99
// release archive checksums in officialCLISpecs. A filename, --version output,
// installation manifest or model argument is not evidence of refresh safety.
var concurrentLarkCLIDigests = map[string]string{
	"darwin/amd64": "cda6b35bd4d5fa2ae869627e748e1d27b3208847c40d19215ccdcccb7b48a1b4",
	"darwin/arm64": "a8f652c2a62577a152467a983ec60548570fb0a08715e95fa3f579438c81885b",
	"linux/amd64":  "a2f55a5b7cb618f2635e1034f4e8196146c9c055c3995035322ba14382be66ba",
	"linux/arm64":  "0e30f6b4e1d0b887f4b6bf3391c747b5e14c11defc255930a59a7dfa23fabf92",
}

func verifiedConcurrentLarkCLI(c SourceConfig) bool {
	want := concurrentLarkCLIDigests[runtime.GOOS+"/"+runtime.GOARCH]
	raw := c.Env["LARK_MCP_CLI"]
	if want == "" || !filepath.IsAbs(raw) || strings.Contains(raw, "$") {
		return false
	}
	path, err := resolvedProgram(raw)
	if err != nil {
		return false
	}
	declared := false
	for _, candidate := range c.Executables {
		if actual, err := resolvedProgram(candidate); err == nil && actual == path {
			declared = true
			break
		}
	}
	if !declared {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxOfficialCLIBinary+1))
	return err == nil && n <= maxOfficialCLIBinary && hex.EncodeToString(h.Sum(nil)) == want
}

type larkOperationPlan struct {
	shared   bool   // shared maintenance lease; business requests may overlap
	resource string // canonical identity, hashed before it becomes a filename
	write    bool
}

var larkResourceToken = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)

// Resource aliases that need CLI/server resolution (notably Wiki URLs) are
// deliberately exclusive. Different spellings must not evade a write lease.
func larkResource(raw, family string) string {
	if larkResourceToken.MatchString(raw) {
		return family + ":" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || !larkResourceToken.MatchString(parts[1]) {
		return ""
	}
	allowed := family == "document" && (parts[0] == "docx" || parts[0] == "docs" || parts[0] == "doc") ||
		family == "sheet" && parts[0] == "sheets" || family == "base" && parts[0] == "base"
	if !allowed {
		return ""
	}
	return family + ":" + parts[1]
}

func larkCallPlan(c extension.Call) larkOperationPlan {
	var in struct {
		Operation string         `json:"operation"`
		Options   map[string]any `json:"options"`
		Service   string         `json:"service"`
		Command   []string       `json:"command"`
		Argv      []string       `json:"argv"`
	}
	if json.Unmarshal(c.Arguments, &in) != nil {
		return larkOperationPlan{}
	}
	option := func(key string) string { value, _ := in.Options[key].(string); return value }
	p := larkOperationPlan{shared: true}
	switch c.Tool {
	case "doc_search", "chat_search", "message_search", "chat_history":
		return p
	case "doc_write":
		p.write = true
		if in.Operation == "create" {
			return p // the server allocates a new resource
		}
		fallthrough
	case "doc_read":
		p.resource = larkResource(option("doc"), "document")
	case "sheet_write", "sheet_structure":
		p.write = true
		if c.Tool == "sheet_write" && in.Operation == "table" {
			return larkOperationPlan{} // nested payload target semantics are conservative
		}
		fallthrough
	case "sheet_read":
		p.resource = larkResource(option("spreadsheet_token"), "sheet")
		if option("url") != "" {
			p.resource = larkResource(option("url"), "sheet")
		}
	case "base_record_write":
		p.write = true
		fallthrough
	case "base_schema", "base_record_search":
		// Lock the entire Base: field/schema and record operations can interact.
		p.resource = larkResource(option("base_token"), "base")
	case "message_send":
		p.write = true
		id := option("chat_id")
		if id != "" {
			p.resource = "chat:" + id
		} else if id = option("user_id"); id != "" {
			p.resource = "user:" + id
		}
	case "cli_help":
		p.shared = in.Service == "lark" && len(in.Command) > 0 && len(in.Command) <= 2
		return p
	case "cli_call":
		p.shared = in.Service == "lark" && len(in.Argv) >= 2 && in.Argv[0] == "drive" && in.Argv[1] == "+search"
		return p
	default:
		// Meegle, combined status, unknown long-tail operations and future tools
		// remain source-exclusive until their local-state behavior is audited.
		return larkOperationPlan{}
	}
	if p.resource == "" {
		return larkOperationPlan{}
	}
	return p
}

// All locks live beside HOME and survive reset. Slots are separate flock
// descriptors, so the bound applies across Managers, GUI/TUI and OS processes.
func acquireLarkLease(ctx context.Context, home string, p larkOperationPlan, report func(extension.Progress)) (func(), error) {
	parent := filepath.Dir(home)
	var releases []func()
	var once sync.Once
	release := func() {
		once.Do(func() {
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
		})
	}
	fail := func(err error) (func(), error) { release(); return nil, err }
	acquire := func(path string, shared bool) error {
		unlock, err := stateLockMode(nil, path, shared)
		if errors.Is(err, errStateInUse) {
			if report != nil {
				report(extension.Progress{Message: "Waiting for another Lark operation (maintenance or resource conflict)…"})
			}
			unlock, err = stateLockMode(ctx, path, shared)
		}
		if err == nil {
			releases = append(releases, unlock)
		}
		return err
	}
	if err := acquire(filepath.Join(parent, ".lock"), p.shared); err != nil {
		return fail(err)
	}
	if !p.shared {
		return release, nil
	}
	if p.resource != "" {
		digest := sha256.Sum256([]byte(p.resource))
		if err := acquire(filepath.Join(parent, fmt.Sprintf(".resource-%x.lock", digest)), !p.write); err != nil {
			return fail(err)
		}
	}
	announced := false
	for {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		for slot := range larkParallelLimit {
			unlock, err := lockState(filepath.Join(parent, fmt.Sprintf(".operation-%d.lock", slot)))
			if err == nil {
				releases = append(releases, unlock)
				return release, nil
			}
			if !errors.Is(err, errStateInUse) {
				return fail(err)
			}
		}
		if !announced && report != nil {
			report(extension.Progress{Message: "Waiting for a free Lark concurrency slot…"})
			announced = true
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fail(ctx.Err())
		case <-timer.C:
		}
	}
}
