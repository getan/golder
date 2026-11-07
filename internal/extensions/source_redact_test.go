package extensions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/extension"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSourceSecretsAreWithheldEvenWithoutGlobalRedactor(t *testing.T) {
	secret := "fixture-" + `secret"value`
	var logs logBuffer
	logs.AddSecrets("Bearer " + secret)
	_, _ = logs.Write([]byte("server echoed " + secret[:5]))
	_, _ = logs.Write([]byte(secret[5:]))
	if strings.Contains(logs.String(), secret) {
		t.Fatal("source log exposed a configured credential")
	}
	structured, err := json.Marshal(map[string]any{"nested": []any{secret}})
	if err != nil {
		t.Fatal(err)
	}
	if !logs.ContainsSecretJSON(structured) {
		t.Fatal("JSON escaping concealed a credential from detection")
	}
	result := agentResultForSource(extension.Result{
		Content:           []extension.ContentBlock{{Type: "text", Text: "token " + secret}},
		StructuredContent: structured,
	}, &logs)
	raw, err := json.Marshal(result)
	if err != nil || strings.Contains(string(raw), secret) || strings.Contains(string(raw), "secret\\\"value") || result.Details != nil {
		t.Fatal("tool output or structured details exposed a credential")
	}
}

func TestMCPCatalogContainingCredentialIsRejected(t *testing.T) {
	secret := "fixture-" + "opaque-credential"
	server := mcp.NewServer(&mcp.Implementation{Name: "redaction-test", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "example", Description: "echo " + secret, InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true})
	host := httptest.NewServer(handler)
	defer host.Close()
	ref := SourceRef{Kind: "mcp", ID: "sensitive"}
	manager := testManager(t, ref, SourceConfig{Enabled: true, Transport: "http", URL: host.URL, Headers: map[string]string{"Authorization": "Bearer " + secret}}, nil)
	defer manager.Close()
	if err := manager.Approve(ref); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := manager.Connect(ctx, ref)
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "withheld") {
		t.Fatalf("unsafe catalog was not rejected safely: %v", err)
	}
	if len(manager.Tools()) != 0 {
		t.Fatal("unsafe catalog became model-visible")
	}
}

func TestMissingCredentialWithholdsCachedCatalog(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	var logs logBuffer
	logs.registerConfigSecrets(SourceConfig{Headers: map[string]string{"Authorization": "${credential:does-not-exist}"}})
	if logs.safeCatalog(Catalog{Protocol: "mcp"}) {
		t.Fatal("catalog was admitted with an unresolved credential")
	}
}
