package androiduse

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

func TestToolSendsObserveToUnixSocket(t *testing.T) {
	dir := t.TempDir()
	endpointPath := filepath.Join(dir, "android.json")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if err := os.WriteFile(endpointPath, []byte(`{"port":`+strconv.Itoa(port)+`,"token":"test-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	requests := make(chan map[string]any, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request map[string]any
		if err := json.NewDecoder(conn).Decode(&request); err != nil {
			return
		}
		requests <- request
		_ = json.NewEncoder(conn).Encode(map[string]any{
			"ok": true, "snapshotId": "s-1", "nodes": []any{},
		})
	}()

	tool := NewTool(endpointPath)
	result, err := tool.Execute(context.Background(), "call-1", json.RawMessage(`{"action":"observe"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %+v", result.Content)
	}
	select {
	case request := <-requests:
		if request["action"] != "observe" || request["token"] != "test-token" {
			t.Fatalf("request = %+v", request)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive request")
	}
}

func TestToolRejectsMissingSnapshotForNodeAction(t *testing.T) {
	tool := NewTool("/nonexistent")
	result, err := tool.Execute(context.Background(), "call-1", json.RawMessage(`{"action":"tap","ref":"n-1"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected missing snapshot to fail")
	}
}

func TestToolRejectsInvalidPackage(t *testing.T) {
	tool := NewTool("/nonexistent")
	result, err := tool.Execute(context.Background(), "call-1", json.RawMessage(`{"action":"open_app","packageName":"../bad"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected invalid package to fail")
	}
}

func TestToolStatusRoundTrip(t *testing.T) {
	dir := t.TempDir()
	endpointPath := filepath.Join(dir, "android.json")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if err := os.WriteFile(endpointPath, []byte(`{"port":`+strconv.Itoa(port)+`,"token":"status-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request map[string]any
		if err := json.NewDecoder(conn).Decode(&request); err != nil {
			return
		}
		_ = json.NewEncoder(conn).Encode(map[string]any{
			"ok": true, "action": "status", "paused": false, "controlAllowed": true,
		})
	}()

	tool := NewTool(endpointPath)
	result, err := tool.Execute(context.Background(), "call-status", json.RawMessage(`{"action":"status"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %+v", result.Content)
	}
}

func TestToolPhase2ActionsRequireOptIn(t *testing.T) {
	tool := NewTool("/nonexistent")
	result, err := tool.Execute(context.Background(), "background-off", json.RawMessage(`{"action":"background_status"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected phase 2 action to be rejected without opt-in")
	}
}

func TestToolPhase2SchemaIncludesSessionActions(t *testing.T) {
	tool := NewToolWithPhase2("/nonexistent", true)
	schema := string(tool.Schema())
	for _, action := range []string{
		"background_status", "background_start", "background_stop",
		"background_open_app", "background_tap", "background_release",
	} {
		if !strings.Contains(schema, `"`+action+`"`) {
			t.Fatalf("schema is missing %s", action)
		}
	}
	if strings.Contains(schema, "phase2_") || strings.Contains(tool.Description(), "Phase 2") {
		t.Fatal("development phase names must not be exposed to the model")
	}
}

func TestToolPhase2TapValidatesCoordinates(t *testing.T) {
	tool := NewToolWithPhase2("/nonexistent", true)
	result, err := tool.Execute(context.Background(), "background-tap", json.RawMessage(`{"action":"background_tap"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected phase 2 tap without coordinates to fail")
	}
}

func TestBackgroundStatusUsesInternalProtocolAndReturnsPublicName(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	endpointPath := filepath.Join(t.TempDir(), "android.json")
	port := listener.Addr().(*net.TCPAddr).Port
	if err := os.WriteFile(endpointPath, []byte(`{"port":`+strconv.Itoa(port)+`,"token":"test-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	requests := make(chan map[string]any, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request map[string]any
		if json.NewDecoder(conn).Decode(&request) != nil {
			return
		}
		requests <- request
		_ = json.NewEncoder(conn).Encode(map[string]any{
			"ok": true, "action": "phase2_status", "advanced": true, "paired": true,
		})
	}()
	result, err := NewToolWithPhase2(endpointPath, true).Execute(context.Background(), "status", json.RawMessage(`{"action":"background_status"}`), nil)
	if err != nil || result.IsError {
		t.Fatalf("background status failed: result=%+v err=%v", result, err)
	}
	if request := <-requests; request["action"] != "phase2_status" {
		t.Fatalf("internal request = %+v", request)
	}
	text := agentcore.ContentToText(result.Content)
	if !strings.Contains(text, `"action":"background_status"`) || strings.Contains(text, "phase2_") {
		t.Fatalf("public result = %s", text)
	}
}
