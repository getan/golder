// Command 06-conversation shows multi-turn state: a single session remembers the
// earlier exchange, so a follow-up prompt can refer back to it. Reset starts a
// fresh conversation on the same session.
//
//	export ANTHROPIC_API_KEY=sk-...
//	go run ./examples/sdk/06-conversation
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
		agent.WithoutTools(),
	)
	if err != nil {
		log.Fatalf("create session: %v", err)
	}
	defer sess.Close()

	ctx := context.Background()

	// First turn establishes context.
	first, err := sess.Prompt(ctx, "My favorite number is 7. Remember it.")
	if err != nil {
		log.Fatalf("turn 1: %v", err)
	}
	fmt.Println("A:", first)

	// Second turn relies on the remembered history.
	second, err := sess.Prompt(ctx, "What is my favorite number times 6?")
	if err != nil {
		log.Fatalf("turn 2: %v", err)
	}
	fmt.Println("A:", second)

	// Reset clears the history; the model no longer knows the favorite number.
	sess.Reset()
	third, err := sess.Prompt(ctx, "What is my favorite number?")
	if err != nil {
		log.Fatalf("turn 3: %v", err)
	}
	fmt.Println("A (after reset):", third)
}
