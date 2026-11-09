package appsession

import (
	"context"
	"testing"

	"github.com/getan/golder/internal/runtime"
)

func TestSlashPromptExpandsArgumentsAndStartsRun(t *testing.T) {
	c, p := newSteeringController(t)
	c.slash.AddUser(runtime.SlashCommand{Name: "greet", Expand: func(args string) string { return "hello " + args }})
	if _, err := c.RunAction(context.Background(), "greet", "alice bob"); err != nil {
		t.Fatal(err)
	}
	request := waitSteeringRequest(t, p)
	if len(request) != 1 || request[0] != "user:hello alice bob" {
		t.Fatalf("slash prompt = %v", request)
	}
	close(p.releases[0])
	waitSteeringEvent(t, c, EventRunEnd, "")
}
