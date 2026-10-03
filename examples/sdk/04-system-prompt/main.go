// Command 04-system-prompt customizes the agent's behavior with a system prompt.
// WithSystemPrompt replaces golder's built-in instruction entirely; use
// WithAppendSystemPrompt instead to keep the built-in instruction and add to it.
//
//	export ANTHROPIC_API_KEY=sk-...
//	go run ./examples/sdk/04-system-prompt
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/getan/golder/agent"
)

func main() {
	sess, err := agent.New(
		agent.WithModel("claude-opus-4-8"),
		agent.WithAPIKey(os.Getenv("ANTHROPIC_API_KEY")),
		agent.WithSystemPrompt("You are a terse assistant. Always answer like a pirate, in one short sentence."),
		// Tools are irrelevant for this persona demo, so drop them for a pure
		// text completion.
		agent.WithoutTools(),
	)
	if err != nil {
		log.Fatalf("create session: %v", err)
	}
	defer sess.Close()

	reply, err := sess.Prompt(context.Background(), "How is the weather today?")
	if err != nil {
		log.Fatalf("prompt: %v", err)
	}
	fmt.Println(reply)
}
