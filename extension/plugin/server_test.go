package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/extension"
)

func TestV1HandshakeAndToolResult(t *testing.T) {
	input := strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"echo\",\"arguments\":{}}}\n")
	var out bytes.Buffer
	e := ServeIO(context.Background(), Spec{ID: "demo", Tools: []Tool{{Spec: extension.ToolSpec{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handle: func(context.Context, extension.Call, func(extension.Progress)) (extension.Result, error) {
		r := extension.Text("hello")
		r.IsError = true
		return r, nil
	}}}}, input, &out)
	if e != nil {
		t.Fatal(e)
	}
	dec := json.NewDecoder(&out)
	var first, second struct {
		Result map[string]any `json:"result"`
	}
	if dec.Decode(&first) != nil || dec.Decode(&second) != nil {
		t.Fatal(out.String())
	}
	if first.Result["name"] != "demo" || second.Result["content"] != "hello" || second.Result["isError"] != true {
		t.Fatal(first, second)
	}
}

func TestSpecValidation(t *testing.T) {
	if ServeIO(context.Background(), Spec{ID: "demo", APIVersion: "99"}, strings.NewReader(""), &bytes.Buffer{}) == nil {
		t.Fatal("accepted unsupported API version")
	}
}
