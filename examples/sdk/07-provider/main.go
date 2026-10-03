// Command 07-provider points the SDK at a non-default provider. There are two
// common ways to do it: select a named provider from your golder config with
// WithProvider, or target any OpenAI-compatible endpoint directly with
// WithBaseURL + WithProtocol. This example uses the latter against OpenRouter.
//
//	export OPENROUTER_API_KEY=sk-or-...
//	go run ./examples/sdk/07-provider
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/getan/golder/agent"
)

func main() {
	// Talk to an OpenAI-compatible endpoint explicitly. WithProtocol tells the
	// SDK the wire format; "anthropic" is the other accepted value.
	sess, err := agent.New(
		agent.WithModel("openrouter/auto"),
		agent.WithBaseURL("https://openrouter.ai/api/v1"),
		agent.WithProtocol("openai"),
		agent.WithAPIKey(os.Getenv("OPENROUTER_API_KEY")),
		agent.WithoutTools(),
	)
	if err != nil {
		log.Fatalf("create session: %v", err)
	}
	defer sess.Close()

	fmt.Printf("provider=%s model=%s\n\n", sess.Provider(), sess.Model())

	reply, err := sess.Prompt(context.Background(), "Reply with exactly: ok")
	if err != nil {
		log.Fatalf("prompt: %v", err)
	}
	fmt.Println(reply)
}
