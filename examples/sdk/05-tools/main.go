// Command 05-tools demonstrates the tool policy: restrict the agent to an
// allowlist, remove specific tools with a denylist (deny always wins), and
// inspect the resulting tool set with ToolNames.
//
// Because tools are auto-executed, a read-only allowlist is a simple way to let
// an agent inspect a codebase without being able to modify it.
//
//	export ANTHROPIC_API_KEY=sk-...
//	go run ./examples/sdk/05-tools
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/getan/golder/agent"
)

func main() {
	// Read-only agent: only the file-inspection tools, and bash explicitly
	// denied even though it is not in the allowlist (belt and suspenders — deny
	// wins regardless).
	sess, err := agent.New(
		agent.WithModel("claude-opus-4-8"),
		agent.WithAPIKey(os.Getenv("ANTHROPIC_API_KEY")),
		agent.WithTools("read", "grep", "find"),
		agent.WithDisallowedTools("bash"),
	)
	if err != nil {
		log.Fatalf("create session: %v", err)
	}
	defer sess.Close()

	// ToolNames reflects the applied policy — a quick way to confirm the agent
	// can do only what you intended.
	fmt.Printf("available tools: %s\n\n", strings.Join(sess.ToolNames(), ", "))

	reply, err := sess.Prompt(context.Background(),
		"List the Go files in the current directory and summarize what this program does.")
	if err != nil {
		log.Fatalf("prompt: %v", err)
	}
	fmt.Println(reply)
}
