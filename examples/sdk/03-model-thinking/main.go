// Command 03-model-thinking shows how to pick a model and set the
// reasoning-effort ("thinking") level. Higher levels let the model reason longer
// before answering, at the cost of more tokens and latency.
//
//	export ANTHROPIC_API_KEY=sk-...
//	go run ./examples/sdk/03-model-thinking
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/getan/golder/agent"
)

func main() {
	// Valid thinking levels: off, minimal, low, medium (default), high, xhigh, max.
	sess, err := agent.New(
		agent.WithModel("claude-opus-4-8"),
		agent.WithAPIKey(os.Getenv("ANTHROPIC_API_KEY")),
		agent.WithThinkingLevel("high"),
	)
	if err != nil {
		log.Fatalf("create session: %v", err)
	}
	defer sess.Close()

	fmt.Printf("model=%s provider=%s\n\n", sess.Model(), sess.Provider())

	reply, err := sess.Prompt(context.Background(),
		"A bat and a ball cost $1.10 together. The bat costs $1.00 more than the ball. How much is the ball? Explain briefly.")
	if err != nil {
		log.Fatalf("prompt: %v", err)
	}
	fmt.Println(reply)
}
