package browserctl

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonReply(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// Quit speaks the documented contract: POST /v1/quit with apiVersion v1, and a
// standard envelope answer of {apiVersion, id:null, ok:true,
// result:{quitting:true}}.
func TestQuitSendsTheStandardRequestAndAcceptsTheStandardReply(t *testing.T) {
	var method, path, body string
	c := NewWithTransport("stub", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		return jsonReply(http.StatusOK, `{"apiVersion":"v1","id":null,"ok":true,"result":{"quitting":true}}`), nil
	}))
	if err := c.Quit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/v1/quit" {
		t.Fatalf("%s %s", method, path)
	}
	var sent struct {
		APIVersion string `json:"apiVersion"`
		ID         string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	if sent.APIVersion != APIVersion || sent.ID == "" {
		t.Fatalf("request = %+v", sent)
	}
}

func TestQuitReportsTheEnvelopeError(t *testing.T) {
	c := NewWithTransport("stub", roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonReply(http.StatusInternalServerError,
			`{"apiVersion":"v1","id":null,"ok":false,"error":{"code":"internal_error","message":"cannot quit","retryable":false}}`), nil
	}))
	err := c.Quit(context.Background())
	var be *Error
	if !errors.As(err, &be) || be.Code != "internal_error" || be.Message != "cannot quit" {
		t.Fatalf("err = %v", err)
	}
}

// "ok" without "quitting" is not acceptance: a close that treated it as one
// would report success for a browser that is still running.
func TestQuitRefusesAnOKReplyThatIsNotQuitting(t *testing.T) {
	c := NewWithTransport("stub", roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonReply(http.StatusOK, `{"apiVersion":"v1","id":null,"ok":true,"result":{"quitting":false}}`), nil
	}))
	if err := c.Quit(context.Background()); err == nil || !strings.Contains(err.Error(), "quitting") {
		t.Fatalf("err = %v", err)
	}
}

func TestQuitRejectsAnotherAPIVersionOrGarbage(t *testing.T) {
	for name, reply := range map[string]*http.Response{
		"apiVersion v2": jsonReply(http.StatusOK, `{"apiVersion":"v2","id":null,"ok":true,"result":{"quitting":true}}`),
		"not json":      jsonReply(http.StatusOK, `<html>hello</html>`),
		"empty":         jsonReply(http.StatusOK, ``),
	} {
		t.Run(name, func(t *testing.T) {
			c := NewWithTransport("stub", roundTripFunc(func(*http.Request) (*http.Response, error) {
				return reply, nil
			}))
			if err := c.Quit(context.Background()); err == nil {
				t.Fatal("accepted a reply outside the contract")
			}
		})
	}
}

// The quit request must stop when the caller's context does, because it runs
// inside a close with a bounded budget.
func TestQuitHonoursContextCancellation(t *testing.T) {
	c := NewWithTransport("stub", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := c.Quit(ctx)
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("elapsed=%s err=%v", time.Since(start), err)
	}
}

func TestInfoHasCapability(t *testing.T) {
	info := Info{Capabilities: []Capability{{Name: "snapshot"}, {Name: CapQuit}}}
	if !info.HasCapability(CapQuit) {
		t.Fatal("quit capability not reported")
	}
	if info.HasCapability("eval") {
		t.Fatal("reported a capability that is not advertised")
	}
	var empty Info
	if empty.HasCapability(CapQuit) {
		t.Fatal("zero Info advertises capabilities")
	}
}

// A legacy browser's info carries no quit capability, which is exactly how the
// close path decides to use the older key sequence.
func TestInfoParsesCapabilitiesFromTheWire(t *testing.T) {
	c := NewWithTransport("stub", roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonReply(http.StatusOK, `{"apiVersion":"v1","browserId":"b1","backend":"native-cdp",
			"tabs":[],"capabilities":[{"name":"quit","description":"Shut down","readOnly":false}]}`), nil
	}))
	info, err := c.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasCapability(CapQuit) {
		t.Fatalf("info = %+v", info)
	}
}

func TestScreenshotTargetPinsTabAndRejectsMismatchedMetadata(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		c := NewWithTransport("stub", roundTripFunc(func(r *http.Request) (*http.Response, error) {
			var request struct {
				Action string `json:"action"`
				Target Target `json:"target"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.Action != "screenshot" || request.Target.TabID != "tab-1" || request.Target.TargetID != "target-1" {
				t.Fatalf("request = %+v", request)
			}
			tab := "tab-1"
			if mismatch {
				tab = "tab-2"
			}
			return jsonReply(http.StatusOK, `{"ok":true,"result":{"mimeType":"image/png","data":"YWJj"},"meta":{"browserId":"b1","tabId":"`+tab+`","targetId":"target-1"}}`), nil
		}))
		data, _, err := c.ScreenshotTarget(context.Background(), Target{BrowserID: "b1", TabID: "tab-1", TargetID: "target-1"})
		if mismatch {
			if err == nil {
				t.Fatal("accepted a screenshot from the wrong tab")
			}
		} else if err != nil || string(data) != "abc" {
			t.Fatalf("screenshot = %q, %v", data, err)
		}
	}
}

func TestTargetedFullPageScreenshotSupportsBoundedLargeInlineReply(t *testing.T) {
	png := bytes.Repeat([]byte{42}, 7<<20)
	encoded := base64.StdEncoding.EncodeToString(png)
	c := NewWithTransport("stub", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var request struct {
			Params struct {
				Full bool `json:"full"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !request.Params.Full {
			t.Fatalf("full-page request = %+v, %v", request, err)
		}
		return jsonReply(http.StatusOK, `{"ok":true,"result":{"mimeType":"image/png","data":"`+encoded+`"},"meta":{"browserId":"b1","tabId":"tab-1","targetId":"target-1"}}`), nil
	}))
	data, _, err := c.ScreenshotTargetPage(context.Background(), Target{BrowserID: "b1", TabID: "tab-1", TargetID: "target-1"}, true)
	if err != nil || !bytes.Equal(data, png) {
		t.Fatalf("large inline screenshot: bytes=%d, err=%v", len(data), err)
	}
}

func TestTargetedScreenshotRejectsOversizedReply(t *testing.T) {
	c := NewWithTransport("stub", roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonReply(http.StatusOK, strings.Repeat("x", maxScreenshotReply+1)), nil
	}))
	_, _, err := c.ScreenshotTarget(context.Background(), Target{TabID: "tab"})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized action reply = %v", err)
	}
}
