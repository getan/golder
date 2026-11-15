// Package androiduse implements the Go side of the Phase 1 AndroidUse
// capability: a typed tool over a local, app-private Unix socket provided by
// the Kotlin AccessibilityService host.
package androiduse

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTimeout    = 20 * time.Second
	maxResponseBytes  = 2 << 20
	maxTextCharacters = 2000
)

// Client talks to the in-process Android host over loopback. The endpoint file
// is app-private and carries a per-process token; no external network is used.
type Client struct {
	Endpoint string
}

type endpoint struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
}

// Do sends one newline-delimited JSON request and decodes one response. A
// response with ok=false is returned as an error; the caller must not replay an
// action whose result is unknown.
func (c Client) Do(ctx context.Context, request map[string]any) (map[string]any, error) {
	if strings.TrimSpace(c.Endpoint) == "" {
		return nil, fmt.Errorf("AndroidUse is not enabled")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}

	raw, err := os.ReadFile(c.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("AndroidUse service is not running: %w", err)
	}
	var endpoint endpoint
	if err := json.Unmarshal(raw, &endpoint); err != nil || endpoint.Port <= 0 || endpoint.Token == "" {
		return nil, fmt.Errorf("AndroidUse endpoint is invalid")
	}

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", "127.0.0.1:"+strconv.Itoa(endpoint.Port))
	if err != nil {
		return nil, fmt.Errorf("AndroidUse service is not running: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	request["token"] = endpoint.Token
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return nil, fmt.Errorf("AndroidUse request: %w", err)
	}
	var response map[string]any
	if err := json.NewDecoder(io.LimitReader(conn, maxResponseBytes)).Decode(&response); err != nil {
		return nil, fmt.Errorf("AndroidUse response: %w", err)
	}
	if ok, _ := response["ok"].(bool); !ok {
		message, _ := response["error"].(string)
		if strings.TrimSpace(message) == "" {
			message = "AndroidUse request failed"
		}
		return nil, fmt.Errorf("%s", message)
	}
	return response, nil
}
