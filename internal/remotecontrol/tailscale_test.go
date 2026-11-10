package remotecontrol

import (
	"context"
	"strings"
	"testing"
)

const sampleTailscaleStatus = `{"BackendState":"Running","Self":{"ID":"node-1","DNSName":"mac.example.ts.net."},"CurrentTailnet":{"Name":"example","StableID":"tailnet-1","MagicDNSEnabled":true},"CertDomains":["mac.example.ts.net"]}`

const sampleServeConfig = `{"TCP":{"443":{"HTTPS":true}},"Web":{"mac.example.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:18787"}}}}}`

func TestParseServeIdentity(t *testing.T) {
	id, err := parseServeIdentity([]byte(sampleTailscaleStatus), []byte(sampleServeConfig))
	if err != nil || id.Origin != "https://mac.example.ts.net" || id.NodeID != "node-1" || id.TailnetID != "tailnet-1" {
		t.Fatalf("identity = %+v, %v", id, err)
	}
}

func TestParseServeIdentityFailsClosed(t *testing.T) {
	tests := map[string][2]string{
		"offline":                    {strings.Replace(sampleTailscaleStatus, "Running", "Stopped", 1), sampleServeConfig},
		"wrong certificate":          {strings.Replace(sampleTailscaleStatus, `"CertDomains":["mac.example.ts.net"]`, `"CertDomains":["other.example.ts.net"]`, 1), sampleServeConfig},
		"not https":                  {sampleTailscaleStatus, strings.Replace(sampleServeConfig, `"HTTPS":true`, `"HTTP":true`, 1)},
		"wrong backend":              {sampleTailscaleStatus, strings.Replace(sampleServeConfig, "127.0.0.1:18787", "192.168.1.1:18787", 1)},
		"funnel":                     {sampleTailscaleStatus, strings.Replace(sampleServeConfig, `{"TCP"`, `{"AllowFunnel":{"mac.example.ts.net:443":true},"TCP"`, 1)},
		"same-origin extra handler":  {sampleTailscaleStatus, strings.Replace(sampleServeConfig, `"/":{"Proxy":"http://127.0.0.1:18787"}`, `"/":{"Proxy":"http://127.0.0.1:18787"},"/other":{"Text":"unsafe"}`, 1)},
		"alternate backend spelling": {sampleTailscaleStatus, strings.Replace(sampleServeConfig, `"Proxy":"http://127.0.0.1:18787"`, `"Proxy":"localhost:18787"`, 1)},
		"unknown routing field":      {sampleTailscaleStatus, strings.Replace(sampleServeConfig, `{"TCP"`, `{"UnknownPublicRoute":true,"TCP"`, 1)},
		"foreground":                 {sampleTailscaleStatus, strings.Replace(sampleServeConfig, `{"TCP"`, `{"Foreground":{"other":{}},"TCP"`, 1)},
		"service":                    {sampleTailscaleStatus, strings.Replace(sampleServeConfig, `{"TCP"`, `{"Services":{"svc:other":{}},"TCP"`, 1)},
		"bad json":                   {sampleTailscaleStatus, `{`},
	}
	for name, pair := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseServeIdentity([]byte(pair[0]), []byte(pair[1])); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}

func TestVerifyTailscaleServeUsesReadOnlyCommands(t *testing.T) {
	var calls []string
	id, err := verifyTailscaleServe(context.Background(), func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if len(calls) == 1 {
			return []byte(sampleTailscaleStatus), nil
		}
		return []byte(sampleServeConfig), nil
	})
	if err != nil || id.Origin == "" || strings.Join(calls, ";") != "status --json;serve status --json" {
		t.Fatalf("identity=%+v calls=%v err=%v", id, calls, err)
	}
}
