//go:build unix

package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getan/golder/extension"
)

func TestLarkConcurrencyPlansAndResourceAliases(t *testing.T) {
	for _, tc := range []struct {
		tool, args, resource string
		shared, write        bool
	}{
		{"doc_search", `{"options":{}}`, "", true, false},
		{"doc_read", `{"options":{"doc":"docABC"}}`, "document:docABC", true, false},
		{"doc_write", `{"options":{"doc":"https://tenant.feishu.cn/docx/docABC?from=copy"}}`, "document:docABC", true, true},
		{"doc_write", `{"operation":"create","options":{"title":"new"}}`, "", true, true},
		{"doc_write", `{"options":{"doc":"https://tenant.feishu.cn/wiki/wikiABC"}}`, "", false, false},
		{"sheet_write", `{"options":{"spreadsheet_token":"sheetABC","sheet_id":"first"}}`, "sheet:sheetABC", true, true},
		{"sheet_structure", `{"options":{"url":"https://tenant.feishu.cn/sheets/sheetABC"}}`, "sheet:sheetABC", true, true},
		{"sheet_read", `{"options":{"url":"https://tenant.feishu.cn/sheets/sheetABC?sheet=second"}}`, "sheet:sheetABC", true, false},
		{"base_record_write", `{"options":{"base_token":"baseABC","table_id":"table1"}}`, "base:baseABC", true, true},
		{"cli_call", `{"service":"lark","argv":["drive","+search","--query="]}`, "", true, false},
		{"cli_call", `{"service":"lark","argv":["docs","+update"]}`, "", false, false},
		{"cli_help", `{"service":"project","command":["workitem","query"]}`, "", false, false},
		{"ticket_get", `{"options":{"work_item_id":"1"}}`, "", false, false},
		{"status", `{}`, "", false, false},
		{"future_tool", `{"options":{"read_only":true}}`, "", false, false},
	} {
		got := larkCallPlan(extension.Call{Tool: tc.tool, Arguments: json.RawMessage(tc.args)})
		if got.shared != tc.shared || got.write != tc.write || got.resource != tc.resource {
			t.Errorf("%s %s: plan=%+v", tc.tool, tc.args, got)
		}
	}
}

func TestLarkSharedLeaseBoundAndMaintenanceExclusion(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var releases []func()
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for range larkParallelLimit {
		release, err := acquireLarkLease(ctx, home, larkOperationPlan{shared: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	if release, err := lockState(filepath.Join(filepath.Dir(home), ".lock")); !errors.Is(err, errStateInUse) {
		if release != nil {
			release()
		}
		t.Fatalf("maintenance entered during shared business calls: %v", err)
	}
	queued, done := make(chan struct{}, 1), make(chan error, 1)
	waitCtx, stop := context.WithCancel(ctx)
	go func() {
		release, err := acquireLarkLease(waitCtx, home, larkOperationPlan{shared: true}, func(extension.Progress) { queued <- struct{}{} })
		if release != nil {
			release()
		}
		done <- err
	}()
	select {
	case <-queued:
	case <-ctx.Done():
		t.Fatal("fifth call did not queue")
	}
	stop()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("slot wait ignored cancellation: %v", err)
	}
	releases[0]()
	release, err := acquireLarkLease(ctx, home, larkOperationPlan{shared: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestLarkResourceLeasesSerializeConflictsNotOtherDocuments(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	ctx := context.Background()
	write := larkOperationPlan{shared: true, resource: "document:same", write: true}
	release, err := acquireLarkLease(ctx, home, write, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	other, err := acquireLarkLease(ctx, home, larkOperationPlan{shared: true, resource: "document:other", write: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	other()
	for _, p := range []larkOperationPlan{write, {shared: true, resource: "document:same"}, {}} {
		waitCtx, stop := context.WithTimeout(ctx, 60*time.Millisecond)
		r, err := acquireLarkLease(waitCtx, home, p, nil)
		stop()
		if r != nil {
			r()
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("conflicting operation entered: plan=%+v err=%v", p, err)
		}
	}
	release()
	read := larkOperationPlan{shared: true, resource: "document:same"}
	a, err := acquireLarkLease(ctx, home, read, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a()
	b, err := acquireLarkLease(ctx, home, read, nil)
	if err != nil {
		t.Fatal(err)
	}
	b()
}

func TestLarkStateBackendActuallyOverlapsWithinAndAcrossBackends(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	cat := Catalog{Tools: []extension.ToolSpec{{Name: "doc_read", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
	entered, release := make(chan string, 6), make(chan struct{})
	var active, peak atomic.Int32
	open := func(context.Context, context.Context) (backend, error) {
		n := active.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		return &leasedLarkFixture{cat: cat, call: func(ctx context.Context, c extension.Call) (extension.Result, error) {
			entered <- c.ID
			select {
			case <-release:
				return extension.Text(c.ID), nil
			case <-ctx.Done():
				return extension.Result{}, ctx.Err()
			}
		}, close: func() error { active.Add(-1); return nil }}, nil
	}
	a, b := larkStateTestBackend(t, open), larkStateTestBackend(t, open)
	for _, backend := range []*larkStateBackend{a, b} {
		backend.concurrentCLI = func() bool { return true } // test-only native fixture
		if _, err := backend.Catalog(ctx); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 5)
	for i := range 5 {
		backend := []*larkStateBackend{a, b}[i%2]
		go func() {
			id := fmt.Sprintf("doc%d", i)
			_, err := backend.Call(ctx, extension.Call{ID: id, Tool: "doc_read", Arguments: json.RawMessage(fmt.Sprintf(`{"options":{"doc":%q}}`, id))}, nil)
			done <- err
		}()
	}
	for range larkParallelLimit {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("independent calls did not overlap before releasing the barrier")
		}
	}
	if peak.Load() != larkParallelLimit {
		t.Fatalf("global concurrency bound is wrong: %d", peak.Load())
	}
	close(release)
	for range 5 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestLarkConcurrencyRequiresPinnedDeclaredNativeBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lark-cli")
	if err := os.WriteFile(path, []byte("not the verified CLI"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, c := range []SourceConfig{
		{Env: map[string]string{"LARK_MCP_CLI": path}, Executables: []string{path}},
		{Env: map[string]string{"LARK_MCP_CLI": path}},
		{Env: map[string]string{"LARK_MCP_CLI": "${UNVERIFIED_CLI}"}, Executables: []string{path}},
	} {
		if verifiedConcurrentLarkCLI(c) {
			t.Fatal("unverified CLI self-assertion enabled concurrent state access")
		}
	}
}
