package androiduse

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
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
