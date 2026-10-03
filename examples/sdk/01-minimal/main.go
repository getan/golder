// Command 01-minimal is the smallest possible golder SDK program: create a
// session, send one prompt, print the reply.
//
//	export ANTHROPIC_API_KEY=sk-...
//	go run ./examples/sdk/01-minimal
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/getan/golder/agent"
)

func main() {
	// The model id selects the provider automatically (here, Anthropic). Tools
	// are enabled and auto-executed by default — see the package docs — so this
	// agent could read or write files if the prompt asked it to.
	sess, err := agent.New(
		agent.WithModel("claude-opus-4-8"),
		agent.WithAPIKey(os.Getenv("ANTHROPIC_API_KEY")),
	)
	if err != nil {
		log.Fatalf("create session: %v", err)
	}
	defer sess.Close()

	reply, err := sess.Prompt(context.Background(), "In one sentence, what is a coding agent?")
	if err != nil {
		log.Fatalf("prompt: %v", err)
	}
	fmt.Println(reply)
}
