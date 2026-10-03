// Command 02-streaming prints the assistant's reply incrementally as it is
// generated, instead of waiting for the whole message.
//
//	export ANTHROPIC_API_KEY=sk-...
//	go run ./examples/sdk/02-streaming
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
	)
	if err != nil {
		log.Fatalf("create session: %v", err)
	}
	defer sess.Close()

	// The callback receives each chunk of assistant text as it arrives. Stream
	// still returns the complete final text, which we ignore here since we have
	// already printed it live.
	_, err = sess.Stream(context.Background(),
		"Count from 1 to 5, one number per line.",
		func(chunk string) { fmt.Print(chunk) },
	)
	if err != nil {
		log.Fatalf("stream: %v", err)
	}
	fmt.Println()
}
