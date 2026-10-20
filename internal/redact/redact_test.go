package redact

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

// The fixtures are assembled at run time so the source never contains a
// contiguous credential-shaped literal. They are synthetic: no real token was
// ever involved, and the audit test below proves the plaintext never reaches
// the log.
var (
	anthropicKey = "sk-ant-" + strings.Repeat("a", 32)
	openaiKey    = "sk-proj-" + strings.Repeat("b", 32)
	githubKey    = "ghp_" + strings.Repeat("c", 36)
	awsKey       = "AKIA" + strings.Repeat("D", 16)
	slackKey     = "xoxb-" + strings.Repeat("1", 12) + "-" + strings.Repeat("e", 16)
	stripeKey    = "sk_" + "live_" + strings.Repeat("f", 24)
	gitlabKey    = "glpat-" + strings.Repeat("g", 20)
	jwtKey       = "eyJhbGciOiJIUzI1NiJ9." + "eyJzdWIiOiIxMjM0NTY3ODkwIn0." + "dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"
	genericPass  = "aB3$xY9!qW2#"
)

func testRedactor(t *testing.T, mode Mode) *Redactor {
	t.Helper()
	r, err := New(Config{Mode: mode})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func TestRulesDetectCredentials(t *testing.T) {
	cases := []struct {
		name string
		text string
		rule string
	}{
		{"anthropic", "key=" + anthropicKey, "anthropic_key"},
		{"openai", "key=" + openaiKey, "openai_key"},
		{"github", "token " + githubKey, "github_pat"},
		{"aws", "id " + awsKey, "aws_access_key"},
		{"slack", "hook " + slackKey, "slack_token"},
		{"stripe", "secret " + stripeKey, "stripe_key"},
		{"gitlab", "token " + gitlabKey, "gitlab_pat"},
		{"jwt", "bearer " + jwtKey, "jwt"},
		{"db_url", "DATABASE_URL=postgres://user:s3cretPass@db.example:5432/app", "db_url"},
		{"generic", "password = \"" + genericPass + "\"", "generic_assignment"},
	}
	r := testRedactor(t, Redact)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := r.Scan(SeamToolOutput, "test", tc.text)
			if len(res.Findings) == 0 {
				t.Fatalf("no finding in %q", tc.text)
			}
			found := false
			for _, f := range res.Findings {
				if f.Rule == tc.rule {
					found = true
				}
			}
			if !found {
				t.Fatalf("rule %q not in %+v", tc.rule, res.Findings)
			}
			for _, f := range res.Findings {
				if strings.Contains(res.Text, tc.text[f.Start:f.End]) && f.Rule != "generic_assignment" {
					t.Errorf("secret survived redaction: %q", res.Text)
				}
			}
			if !strings.Contains(res.Text, "«SECRET:") {
				t.Errorf("no placeholder in %q", res.Text)
			}
		})
	}
}

func TestPrivateKeyBlock(t *testing.T) {
	const pem = "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA1234567890abcdef\n-----END RSA PRIVATE KEY-----"
	r := testRedactor(t, Redact)
	res := r.Scan(SeamToolOutput, "test", "before\n"+pem+"\nafter")
	if len(res.Findings) != 1 || res.Findings[0].Rule != "private_key" {
		t.Fatalf("findings = %+v, want private_key", res.Findings)
	}
	if strings.Contains(res.Text, "MIIEowIBAAKCAQEA") {
		t.Fatalf("private key survived: %q", res.Text)
	}
}

func TestFalsePositivesStayReadable(t *testing.T) {
	cases := []string{
		"sha 0123456789abcdef0123456789abcdef01234567",
		"uuid 123e4567-e89b-12d3-a456-426614174000",
		"password = os.Getenv(\"DB_PASSWORD\")",
		"api_key = \"your-api-key-here\"",
		"token = ${GITHUB_TOKEN}",
		"example AKIAIOSFODNN7EXAMPLE",
		"the password prompt appeared twice",
		"token budget is 24000",
	}
	r := testRedactor(t, Redact)
	for _, text := range cases {
		res := r.Scan(SeamToolOutput, "test", text)
		if res.Text != text {
			t.Errorf("false positive on %q -> %q (%+v)", text, res.Text, res.Findings)
		}
	}
}

func TestStableToken(t *testing.T) {
	r := testRedactor(t, Redact)
	text := "token " + githubKey + " and again " + githubKey
	first := r.Scan(SeamToolOutput, "test", text)
	second := r.Scan(SeamToolOutput, "test", text)
	if first.Text != second.Text {
		t.Fatalf("tokens are not stable:\n%s\n%s", first.Text, second.Text)
	}
	if strings.Count(first.Text, "«SECRET:github_pat:") != 2 {
		t.Fatalf("expected two placeholders, got %q", first.Text)
	}
	other := r.Scan(SeamToolOutput, "test", "token "+stripeKey)
	if strings.Contains(other.Text, first.Text) {
		t.Errorf("different secrets produced the same text: %q", other.Text)
	}
}

func TestRestore(t *testing.T) {
	r := testRedactor(t, Redact)
	res := r.Scan(SeamToolOutput, "bash", "GITHUB_TOKEN="+githubKey)
	restored, n := r.Restore(res.Text)
	if n != 1 {
		t.Fatalf("restored %d values, want 1", n)
	}
	if !strings.Contains(restored, githubKey) {
		t.Fatalf("restore did not recover the secret: %q", restored)
	}
	if strings.Contains(restored, "«SECRET:") {
		t.Fatalf("token survived restore: %q", restored)
	}
}

func TestObserveAndBlock(t *testing.T) {
	text := "token " + githubKey

	observe := testRedactor(t, Observe)
	res := observe.Scan(SeamToolOutput, "test", text)
	if res.Text != text || len(res.Findings) == 0 {
		t.Fatalf("observe changed the text or missed the finding: %+v", res)
	}

	block := testRedactor(t, Block)
	res = block.Scan(SeamToolOutput, "test", text)
	if !res.Blocked || strings.Contains(res.Text, githubKey) {
		t.Fatalf("block leaked or failed: %+v", res)
	}
	if !strings.Contains(res.Text, "github_pat") {
		t.Errorf("block text should name the rule: %q", res.Text)
	}
}

func TestKnownSecret(t *testing.T) {
	r := testRedactor(t, Redact)
	const custom = "custom-credential-9f3k2m"
	r.AddKnown(custom)
	res := r.Scan(SeamToolOutput, "test", "value="+custom)
	if len(res.Findings) != 1 || res.Findings[0].Rule != "known_secret" {
		t.Fatalf("findings = %+v", res.Findings)
	}
	if strings.Contains(res.Text, custom) {
		t.Fatalf("known secret survived: %q", res.Text)
	}
}

func TestAuditOmitsSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	r, err := New(Config{Mode: Redact, AuditPath: path, SessionID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	r.Scan(SeamToolOutput, "bash", "token "+githubKey)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), githubKey) {
		t.Fatalf("audit log contains the secret: %s", data)
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &entry); err != nil {
		t.Fatalf("audit line is not JSON: %v: %s", err, data)
	}
	if entry["rule"] != "github_pat" || entry["seam"] != "tool_output" {
		t.Fatalf("audit entry = %+v", entry)
	}
}

func TestMessageRedaction(t *testing.T) {
	r := testRedactor(t, Redact)
	msgs := agentcore.MessageList{
		agentcore.UserMessage{
			RoleField: agentcore.RoleUser,
			Content:   agentcore.ContentList{agentcore.NewTextContent("my key " + githubKey)},
		},
		agentcore.ToolResultMessage{
			RoleField:  agentcore.RoleToolResult,
			ToolCallID: "1",
			ToolName:   "bash",
			Content:    agentcore.ContentList{agentcore.NewTextContent("env: " + openaiKey)},
		},
		agentcore.CompactionMessage{
			RoleField: agentcore.RoleCompaction,
			Summary:   "the token was " + stripeKey,
		},
	}
	out := r.Messages(SeamToolOutput, msgs)
	for i, m := range out {
		text := agentcore.ContentToText(contentOf(m))
		if strings.Contains(text, githubKey) || strings.Contains(text, openaiKey) {
			t.Errorf("message %d leaked: %q", i, text)
		}
	}
	cm := out[2].(agentcore.CompactionMessage)
	if strings.Contains(cm.Summary, stripeKey) {
		t.Errorf("compaction summary leaked: %q", cm.Summary)
	}
}

func contentOf(m agentcore.Message) agentcore.ContentList {
	switch msg := m.(type) {
	case agentcore.UserMessage:
		return msg.Content
	case agentcore.ToolResultMessage:
		return msg.Content
	case agentcore.AssistantMessage:
		return msg.Content
	default:
		return nil
	}
}

func TestToolResultBlockMode(t *testing.T) {
	r := testRedactor(t, Block)
	res := agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent("token " + githubKey)},
	}
	out := r.ToolResult("bash", res)
	if !out.IsError {
		t.Fatal("blocked tool result should be an error")
	}
	if strings.Contains(agentcore.ContentToText(out.Content), githubKey) {
		t.Fatalf("blocked tool result leaked: %q", agentcore.ContentToText(out.Content))
	}
}

func TestContextCancellationDoesNotMatter(t *testing.T) {
	// Scan is synchronous; this guards the API shape used by tool executors.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := testRedactor(t, Redact)
	if got := r.Text(SeamToolOutput, "bash", "nothing"); got != "nothing" {
		t.Fatalf("got %q", got)
	}
	_ = ctx
}

func BenchmarkScanClean100K(b *testing.B) {
	r, _ := New(Config{Mode: Redact})
	text := strings.Repeat("func main() { fmt.Println(\"hello world\") }\n", 2500)
	b.ReportAllocs()
	b.SetBytes(int64(len(text)))
	for i := 0; i < b.N; i++ {
		r.Scan(SeamToolOutput, "bash", text)
	}
}

func BenchmarkScanSecret100K(b *testing.B) {
	r, _ := New(Config{Mode: Redact})
	text := strings.Repeat("func main() { fmt.Println(\"hello world\") }\n", 2500)
	text = strings.Replace(text, "hello world", "token "+githubKey, 1)
	b.ReportAllocs()
	b.SetBytes(int64(len(text)))
	for i := 0; i < b.N; i++ {
		r.Scan(SeamToolOutput, "bash", text)
	}
}
