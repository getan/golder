package panecontrol

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func testAgent(id, label string) Agent {
	return Agent{ID: id, Label: label, Project: label, Role: "main", ConversationID: "conv-" + id,
		SessionID: "work", PaneID: "agent-" + id, Workspace: 1, Mode: "ask", PID: 1}
}

func testStore(t *testing.T) *Store {
	t.Helper()
	return New(filepath.Join(t.TempDir(), "daemon.sock"))
}

func TestPreviewSurvivesStaleHeartbeatButNotConversationSwitch(t *testing.T) {
	s := testStore(t)
	a := testAgent("a", "golder · Main Agent")
	if err := s.Register(a); err != nil {
		t.Fatal(err)
	}
	current := a
	current.Preview = "修复登录"
	if err := s.Register(current); err != nil {
		t.Fatal(err)
	}
	if err := s.Register(a); err != nil {
		t.Fatal(err)
	}
	agents, err := s.Agents("work", 1)
	if err != nil || len(agents) != 1 || agents[0].Preview != current.Preview {
		t.Fatalf("stale heartbeat removed preview: %+v, %v", agents, err)
	}
	a.ConversationID = "new-conversation"
	if err := s.Register(a); err != nil {
		t.Fatal(err)
	}
	agents, err = s.Agents("work", 1)
	if err != nil || len(agents) != 1 || agents[0].Preview != "" {
		t.Fatalf("previous conversation preview leaked: %+v, %v", agents, err)
	}
}

func TestExclusiveLeaseTransferAndOneCallConsumption(t *testing.T) {
	s := testStore(t)
	a, b := testAgent("a", "annotation-workbench · Main Agent"), testAgent("b", "label-studio · Main Agent")
	for _, agent := range []Agent{a, b} {
		if err := s.Register(agent); err != nil {
			t.Fatal(err)
		}
	}
	base := Lease{SessionID: "work", WindowID: "nas", Workspace: 1, AgentID: a.ID,
		ConversationID: a.ConversationID, Scope: ScopeSession, DaemonID: "daemon"}
	if _, err := s.Claim(ClaimRequest{Lease: base}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authorize("work", "nas", b.ID, b.ConversationID, false); err == nil {
		t.Fatal("second agent received concurrent write authority")
	} else {
		var nc *NotControllerError
		if !errors.As(err, &nc) || nc.Current == nil || nc.Current.AgentID != a.ID {
			t.Fatalf("wrong controller conflict: %v", err)
		}
	}
	transfer := base
	transfer.AgentID, transfer.ConversationID = b.ID, b.ConversationID
	if _, err := s.Claim(ClaimRequest{Lease: transfer}); err == nil {
		t.Fatal("transfer without explicit flag succeeded")
	} else {
		var tr *TransferRequiredError
		if !errors.As(err, &tr) || tr.Current.AgentID != a.ID {
			t.Fatalf("wrong transfer conflict: %v", err)
		}
	}
	if _, err := s.Claim(ClaimRequest{Lease: transfer, Transfer: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authorize("work", "nas", a.ID, a.ConversationID, false); err == nil {
		t.Fatal("old controller retained authority after transfer")
	}
	if _, err := s.Authorize("work", "nas", b.ID, b.ConversationID, false); err != nil {
		t.Fatalf("new controller was not authorized: %v", err)
	}

	once := transfer
	once.Scope = ScopeOnce
	if _, err := s.Claim(ClaimRequest{Lease: once}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authorize("work", "nas", b.ID, b.ConversationID, false); err != nil {
		t.Fatalf("preflight consumed one-call lease: %v", err)
	}
	if _, err := s.Authorize("work", "nas", b.ID, b.ConversationID, true); err != nil {
		t.Fatalf("next operation did not consume lease: %v", err)
	}
	if _, err := s.Authorize("work", "nas", b.ID, b.ConversationID, false); err == nil {
		t.Fatal("one-call lease survived its operation")
	}
}

func TestConcurrentClaimsHaveOneWinner(t *testing.T) {
	s := testStore(t)
	agents := []Agent{testAgent("a", "A"), testAgent("b", "B")}
	for _, agent := range agents {
		if err := s.Register(agent); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, agent := range agents {
		agent := agent
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.Claim(ClaimRequest{Lease: Lease{SessionID: "work", WindowID: "pane", Workspace: 1,
				AgentID: agent.ID, ConversationID: agent.ConversationID, Scope: ScopeSession, DaemonID: "daemon"}})
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
			continue
		}
		var tr *TransferRequiredError
		if errors.As(err, &tr) {
			conflict++
		} else {
			t.Fatalf("unexpected claim error: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d, want one of each", success, conflict)
	}
}

func TestAgentExitAndPaneCloseRevokeLeases(t *testing.T) {
	s := testStore(t)
	a := testAgent("a", "A")
	if err := s.Register(a); err != nil {
		t.Fatal(err)
	}
	claim := func(window string) {
		t.Helper()
		if _, err := s.Claim(ClaimRequest{Lease: Lease{SessionID: "work", WindowID: window, Workspace: 1,
			AgentID: a.ID, ConversationID: a.ConversationID, Scope: ScopeSession, DaemonID: "daemon"}}); err != nil {
			t.Fatal(err)
		}
	}
	claim("closed")
	claim("live")
	if err := s.PruneWindows("work", map[string]bool{"live": true}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get("work", "closed"); ok {
		t.Fatal("closed pane retained a lease")
	}
	if _, ok, _ := s.Get("work", "live"); !ok {
		t.Fatal("live pane lost its lease")
	}
	if err := s.Unregister(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get("work", "live"); ok {
		t.Fatal("agent exit retained its lease")
	}
}

func TestAgentCandidatesExcludeOtherWorkspaceAndReadOnly(t *testing.T) {
	s := testStore(t)
	for _, agent := range []Agent{
		testAgent("eligible", "Eligible"),
		func() Agent { a := testAgent("readonly", "Read only"); a.Mode = "read-only"; return a }(),
		func() Agent { a := testAgent("blocked", "Needs approval"); a.State = "needs_input"; return a }(),
		func() Agent { a := testAgent("other", "Other workspace"); a.Workspace = 2; return a }(),
	} {
		if err := s.Register(agent); err != nil {
			t.Fatal(err)
		}
	}
	agents, err := s.Agents("work", 1)
	if err != nil || len(agents) != 1 || agents[0].ID != "eligible" {
		t.Fatalf("eligible agents = %+v err=%v", agents, err)
	}
}
