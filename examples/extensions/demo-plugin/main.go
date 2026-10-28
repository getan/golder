// A minimal legacy-v1 plugin. Build it with go build, then register the binary
// using golder plugin add; registration alone never starts it.
package main

import (
	"context"
	"encoding/json"
	"log"

	"github.com/getan/golder/extension"
	"github.com/getan/golder/extension/plugin"
)

func main() {
	err := plugin.Serve(context.Background(), plugin.Spec{ID: "demo", Version: "1.0.0", Tools: []plugin.Tool{{Spec: extension.ToolSpec{Name: "echo", Description: "Echo a text value", InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`)}, Handle: func(ctx context.Context, c extension.Call, _ func(extension.Progress)) (extension.Result, error) {
		var args struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(c.Arguments, &args); err != nil {
			return extension.Result{}, err
		}
		return extension.Text(args.Text), ctx.Err()
	}}}})
	if err != nil {
		log.Fatal(err)
	}
}
