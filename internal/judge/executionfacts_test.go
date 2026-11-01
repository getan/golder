package judge

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

func gitFactRepo(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_SSH", "")
	t.Setenv("GIT_SSH_COMMAND", "")
	repo := t.TempDir()
	gitFactCommand(t, repo, "init")
	gitFactCommand(t, repo, "config", "user.email", "facts@example.invalid")
	gitFactCommand(t, repo, "config", "user.name", "Facts Test")
	return repo
}

func gitFactCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func oneExecutionFact(t *testing.T, facts executionFacts) ExecutionFact {
	t.Helper()
	if len(facts.Public) != 1 {
		t.Fatalf("facts = %+v, want one", facts.Public)
	}
	return facts.Public[0]
}

func TestGitPushFactsResolveSSHWithoutLeakingURLCredentials(t *testing.T) {
	repo := gitFactRepo(t)
	gitFactCommand(t, repo, "remote", "add", "origin", "ssh://private-user@GitHub.COM/private/repository.git")
	facts := resolveGitPushFacts(context.Background(), "git push origin refs/tags/v1", repo, runGitFactQuery)
	fact := oneExecutionFact(t, facts)
	if fact.Transport != "ssh" || fact.Host != "github.com" || !fact.NeedsNetwork || !fact.RequiresUnconfined {
		t.Fatalf("SSH fact = %+v", fact)
	}
	if !facts.requiresNetwork || !facts.requiresUnconfined || facts.binding == "" || !facts.blocksBare {
		t.Fatalf("private facts = %+v", facts)
	}
	raw, _ := json.Marshal(facts.Public)
	for _, secret := range []string{"private-user", "private/repository", "ssh://"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("review facts leaked %q: %s", secret, raw)
		}
	}
}

func TestGitPushFactsDistinguishHTTPSAndGitTransport(t *testing.T) {
	for _, tc := range []struct {
		name, remote, transport, host string
	}{
		{"https", "https://person:top-secret@example.com/private/repo.git", "https", "example.com"},
		{"git", "git://git.example.com/public/repo.git", "git", "git.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := gitFactRepo(t)
			gitFactCommand(t, repo, "remote", "add", "origin", tc.remote)
			facts := resolveGitPushFacts(context.Background(), "git push origin main", repo, runGitFactQuery)
			fact := oneExecutionFact(t, facts)
			if fact.Transport != tc.transport || fact.Host != tc.host || !fact.NeedsNetwork || fact.RequiresUnconfined || facts.binding != "" {
				t.Fatalf("fact = %+v private=%+v", fact, facts)
			}
			raw, _ := json.Marshal(facts.Public)
			if strings.Contains(string(raw), "top-secret") || strings.Contains(string(raw), "/private/") {
				t.Fatalf("facts leaked URL material: %s", raw)
			}
		})
	}
}

func TestGitPushFactsDistinguishExplicitLocalFromMissingDefault(t *testing.T) {
	repo := gitFactRepo(t)
	explicit := resolveGitPushFacts(context.Background(), "git push backup.git main", repo, runGitFactQuery)
	if fact := oneExecutionFact(t, explicit); fact.Transport != "file" || fact.Remote != "literal" || fact.Resolution != "resolved" || !explicit.blocksBare {
		t.Fatalf("explicit local fact=%+v private=%+v", fact, explicit)
	}
	implicit := resolveGitPushFacts(context.Background(), "git push", repo, runGitFactQuery)
	if fact := oneExecutionFact(t, implicit); fact.Resolution != "unresolved" || fact.ContainmentReason != "remote_alias_not_resolved" || !implicit.blocksBare {
		t.Fatalf("missing default fact=%+v private=%+v", fact, implicit)
	}
}

func TestGitPushFactsUsePushURLAndPushInsteadOf(t *testing.T) {
	t.Run("pushurl wins", func(t *testing.T) {
		repo := gitFactRepo(t)
		gitFactCommand(t, repo, "remote", "add", "origin", "https://read.example.com/org/repo.git")
		gitFactCommand(t, repo, "remote", "set-url", "--push", "origin", "git@write.example.com:org/repo.git")
		fact := oneExecutionFact(t, resolveGitPushFacts(context.Background(), "git push origin main", repo, runGitFactQuery))
		if fact.Transport != "ssh" || fact.Host != "write.example.com" {
			t.Fatalf("pushurl fact = %+v", fact)
		}
	})
	t.Run("pushInsteadOf is effective", func(t *testing.T) {
		repo := gitFactRepo(t)
		gitFactCommand(t, repo, "config", "url.ssh://git@rewrite.example.com/.pushInsteadOf", "https://mirror.example.com/")
		gitFactCommand(t, repo, "remote", "add", "origin", "https://mirror.example.com/org/repo.git")
		fact := oneExecutionFact(t, resolveGitPushFacts(context.Background(), "git push origin main", repo, runGitFactQuery))
		if fact.Transport != "ssh" || fact.Host != "rewrite.example.com" {
			t.Fatalf("rewrite fact = %+v", fact)
		}
	})
}

func TestGitPushFactsResolveLinkedWorktreeAndDefaultRemote(t *testing.T) {
	repo := gitFactRepo(t)
	gitFactCommand(t, repo, "remote", "add", "publish", "git@worktree.example.com:org/repo.git")
	gitFactCommand(t, repo, "commit", "--allow-empty", "-m", "seed")
	worktree := filepath.Join(t.TempDir(), "linked")
	gitFactCommand(t, repo, "worktree", "add", "-b", "publish-branch", worktree)
	gitFactCommand(t, worktree, "config", "branch.publish-branch.pushRemote", "publish")
	fact := oneExecutionFact(t, resolveGitPushFacts(context.Background(), "git push", worktree, runGitFactQuery))
	if fact.Transport != "ssh" || fact.Host != "worktree.example.com" || fact.Remote != "publish" {
		t.Fatalf("worktree fact = %+v", fact)
	}
}

func TestGitPushFactsKeepOpaqueTransportsContained(t *testing.T) {
	for _, tc := range []struct {
		name, remote string
		configure    func(*testing.T, string)
	}{
		{"remote helper", "ext::sh -c dangerous", nil},
		{"custom SSH command", "git@github.com:org/repo.git", func(t *testing.T, repo string) {
			gitFactCommand(t, repo, "config", "core.sshCommand", "sh -c dangerous")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := gitFactRepo(t)
			gitFactCommand(t, repo, "remote", "add", "origin", tc.remote)
			if tc.configure != nil {
				tc.configure(t, repo)
			}
			facts := resolveGitPushFacts(context.Background(), "git push origin main", repo, runGitFactQuery)
			fact := oneExecutionFact(t, facts)
			if fact.Resolution != "opaque" || !facts.blocksBare || facts.requiresUnconfined || facts.binding != "" {
				t.Fatalf("opaque fact = %+v private=%+v", fact, facts)
			}
		})
	}
}

func TestGitPushFactsDeclineAmbiguousCommands(t *testing.T) {
	repo := gitFactRepo(t)
	gitFactCommand(t, repo, "remote", "add", "origin", "git@github.com:org/repo.git")
	for _, command := range []string{
		"git -c core.sshCommand=evil push origin main",
		"cd .. && git push origin main",
		"git push $REMOTE main",
		"env git push origin main",
		"git push origin main | cat",
	} {
		facts := resolveGitPushFacts(context.Background(), command, repo, runGitFactQuery)
		if len(facts.Public) != 1 || facts.Public[0].Resolution != "unresolved" || !facts.blocksBare || facts.requiresUnconfined {
			t.Errorf("%q facts = %+v private=%+v", command, facts.Public, facts)
		}
	}
}

func TestGitPushFactsRejectShellGitOverride(t *testing.T) {
	repo := gitFactRepo(t)
	gitFactCommand(t, repo, "remote", "add", "origin", "git@github.com:org/repo.git")
	t.Setenv("BASH_ENV", filepath.Join(repo, "shell-init"))
	facts := resolveGitPushFacts(context.Background(), "git push origin main", repo, func(context.Context, string, ...string) ([]string, int, error) {
		t.Fatal("shell override must be rejected before invoking Git for facts")
		return nil, 0, nil
	})
	if fact := oneExecutionFact(t, facts); fact.ContainmentReason != "shell_git_override" || !facts.blocksBare || facts.binding != "" {
		t.Fatalf("override fact=%+v private=%+v", fact, facts)
	}
}

func TestGitPushExecutionBindingDetectsRemoteChange(t *testing.T) {
	repo := gitFactRepo(t)
	gitFactCommand(t, repo, "remote", "add", "origin", "git@one.example.com:org/repo.git")
	args := json.RawMessage(`{"command":"git push origin main"}`)
	facts := resolveExecutionFacts(context.Background(), agentcore.AgentToolCall{Name: "bash", Arguments: args}, repo)
	if facts.binding == "" {
		t.Fatal("SSH push has no binding")
	}
	if err := VerifyExecutionBinding(context.Background(), "bash", args, repo, facts.binding); err != nil {
		t.Fatal(err)
	}
	gitFactCommand(t, repo, "remote", "set-url", "origin", "git@two.example.com:org/repo.git")
	if err := VerifyExecutionBinding(context.Background(), "bash", args, repo, facts.binding); err == nil {
		t.Fatal("changed remote kept the old authorization binding")
	}
}

func TestGitPushExecutionBindingCoversLocalPrePushHook(t *testing.T) {
	repo := gitFactRepo(t)
	gitFactCommand(t, repo, "remote", "add", "origin", "git@hooks.example.com:org/repo.git")
	hook := filepath.Join(repo, ".git", "hooks", "pre-push")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho first\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"command":"git push origin main"}`)
	facts := resolveExecutionFacts(context.Background(), agentcore.AgentToolCall{Name: "bash", Arguments: args}, repo)
	if fact := oneExecutionFact(t, facts); fact.LocalHook != "present" || facts.binding == "" {
		t.Fatalf("hook fact=%+v private=%+v", fact, facts)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho changed\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := VerifyExecutionBinding(context.Background(), "bash", args, repo, facts.binding); err == nil {
		t.Fatal("changed pre-push hook kept the old outside-sandbox authorization")
	}

	noVerifyArgs := json.RawMessage(`{"command":"git push --no-verify origin main"}`)
	noVerify := resolveExecutionFacts(context.Background(), agentcore.AgentToolCall{Name: "bash", Arguments: noVerifyArgs}, repo)
	if fact := oneExecutionFact(t, noVerify); fact.LocalHook != "disabled" || noVerify.binding == "" {
		t.Fatalf("--no-verify hook fact=%+v private=%+v", fact, noVerify)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho changed-again\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := VerifyExecutionBinding(context.Background(), "bash", noVerifyArgs, repo, noVerify.binding); err != nil {
		t.Fatalf("disabled hook affected binding: %v", err)
	}
}

func TestGitPushFactsDoNotReadExternalHookPath(t *testing.T) {
	repo := gitFactRepo(t)
	gitFactCommand(t, repo, "remote", "add", "origin", "git@hooks.example.com:org/repo.git")
	external := t.TempDir()
	hook := filepath.Join(external, "pre-push")
	secret := "must-not-enter-review-state"
	if err := os.WriteFile(hook, []byte("#!/bin/sh\n# "+secret+"\n"), 0o111); err != nil {
		t.Fatal(err)
	}
	gitFactCommand(t, repo, "config", "core.hooksPath", external)
	facts := resolveGitPushFacts(context.Background(), "git push origin main", repo, runGitFactQuery)
	fact := oneExecutionFact(t, facts)
	if fact.LocalHook != "external" || fact.Resolution != "opaque" || !facts.blocksBare || facts.binding != "" {
		t.Fatalf("external hook fact=%+v private=%+v", fact, facts)
	}
	raw, _ := json.Marshal(facts.Public)
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), external) {
		t.Fatalf("external hook content/path leaked: %s", raw)
	}
}

func TestGitPushFactResolutionUsesBoundedLocalQueries(t *testing.T) {
	dir := t.TempDir()
	hook := filepath.Join(dir, "missing-pre-push")
	var calls [][]string
	query := func(_ context.Context, _ string, args ...string) ([]string, int, error) {
		calls = append(calls, append([]string(nil), args...))
		switch strings.Join(args, " ") {
		case "remote get-url --push --all -- origin":
			return []string{"git@github.com:org/repo.git"}, 0, nil
		case "config --get core.sshCommand":
			return nil, 1, &exec.ExitError{}
		case "rev-parse --git-path hooks/pre-push":
			return []string{hook}, 0, nil
		default:
			return nil, 1, &exec.ExitError{}
		}
	}
	facts := resolveGitPushFacts(context.Background(), "git push origin main", dir, query)
	if !facts.requiresUnconfined || len(calls) != 3 {
		t.Fatalf("facts=%+v calls=%v", facts, calls)
	}
	for _, call := range calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "credential") || len(call) > 0 && call[0] == "push" {
			t.Fatalf("fact resolver ran an effectful Git command: %v", call)
		}
	}
}

func TestGitPushFactResolutionRefusesProjectGitExecutable(t *testing.T) {
	repo := t.TempDir()
	marker := filepath.Join(repo, "executed")
	fake := filepath.Join(repo, "git")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", repo)
	t.Setenv("GODEBUG", "execerrdot=0")
	if _, _, err := runGitFactQuery(context.Background(), repo, "remote", "get-url", "origin"); err == nil {
		t.Fatal("project-provided git executable was accepted")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("project-provided git executable ran before authorization")
	}
}

func TestGitPushFactsFeedReviewerAndCapabilityGate(t *testing.T) {
	repo := gitFactRepo(t)
	gitFactCommand(t, repo, "remote", "add", "origin", "git@review.example.com:org/repo.git")
	call := agentcore.AgentToolCall{ID: "push", Name: "bash", Arguments: json.RawMessage(`{"command":"git push origin main"}`)}

	t.Run("review state", func(t *testing.T) {
		stub := &reviewStub{answer: `{"level":"confirm","risk":"medium","authorization":"high","rationale":"bounded push","network":"allow"}`}
		j := NewLLMJudge(stub.fn, repo, true)
		ctx := withExecutionFacts(context.Background(), resolveExecutionFacts(context.Background(), call, repo))
		v := j.Classify(ctx, call.Name, call.Arguments)
		if v.Failed || !v.Network || !strings.Contains(stub.user, `"transport":"ssh"`) || !strings.Contains(stub.user, `"requiresUnconfined":true`) {
			t.Fatalf("verdict=%+v state=%s", v, stub.user)
		}
		if strings.Contains(stub.user, "org/repo") || strings.Contains(stub.user, "git@") {
			t.Fatalf("review state leaked effective URL: %s", stub.user)
		}
	})

	t.Run("auto confirm", func(t *testing.T) {
		gate := ConsentGate(ConsentOpts{Mode: permissions.New(permissions.Auto), Cwd: repo,
			Classifier: consentClassifier{v: Verdict{Level: Confirm, Network: true, Source: "llm", Reasons: []string{"bounded push"}}},
			Sandboxed:  func(string) bool { return true }})
		dec := gate(context.Background(), call)
		if dec == nil || dec.Block || !dec.Direct || !dec.Network || dec.ExecutionBinding == "" || dec.Sandbox {
			t.Fatalf("decision = %+v", dec)
		}
	})

	t.Run("auto contain blocks rather than doomed retry", func(t *testing.T) {
		gate := ConsentGate(ConsentOpts{Mode: permissions.New(permissions.Auto), Cwd: repo,
			Classifier: consentClassifier{v: Verdict{Level: Contain, Network: true, Source: "llm"}},
			Sandboxed:  func(string) bool { return true }})
		if dec := gate(context.Background(), call); dec == nil || !dec.Block || dec.Direct {
			t.Fatalf("contain decision = %+v", dec)
		}
	})

	t.Run("HTTPS stays in file sandbox", func(t *testing.T) {
		httpsRepo := gitFactRepo(t)
		gitFactCommand(t, httpsRepo, "remote", "add", "origin", "https://example.com/org/repo.git")
		httpsCall := agentcore.AgentToolCall{ID: "https", Name: "bash", Arguments: json.RawMessage(`{"command":"git push origin main"}`)}
		gate := ConsentGate(ConsentOpts{Mode: permissions.New(permissions.Auto), Cwd: httpsRepo,
			Classifier: consentClassifier{v: Verdict{Level: Allow, Network: true, Source: "llm"}}, Sandboxed: func(string) bool { return true }})
		dec := gate(context.Background(), httpsCall)
		if dec == nil || dec.Block || dec.Direct || dec.Bare || !dec.Network {
			t.Fatalf("HTTPS decision=%+v", dec)
		}
	})

	t.Run("ask shows one bounded escape", func(t *testing.T) {
		asked := 0
		gate := ConsentGate(ConsentOpts{Mode: permissions.New(permissions.Ask), Cwd: repo,
			Classifier: consentClassifier{v: Verdict{Level: Confirm, Network: true, Source: "llm"}},
			Sandboxed:  func(string) bool { return true },
			Confirm: func(_ context.Context, req ConsentRequest) ConsentAnswer {
				asked++
				if req.CanContain || req.RequestedExec != ExecUnconfined || !hasNeed(req.Needs, CapUnconfined) {
					t.Fatalf("request = %+v", req)
				}
				return ConsentAnswer{Allow: true, Answered: true, Scope: ScopeOnce, Needs: req.Needs, Exec: ExecUnconfined}
			}})
		dec := gate(context.Background(), call)
		if asked != 1 || dec == nil || !dec.Direct || dec.ExecutionBinding == "" {
			t.Fatalf("asked=%d decision=%+v", asked, dec)
		}
	})

	t.Run("explicit escalation does not duplicate derived escape", func(t *testing.T) {
		explicit := call
		explicit.Arguments = json.RawMessage(`{"command":"git push origin main","network":"on","sandbox_permissions":"require_escalated","justification":"origin is SSH"}`)
		gate := ConsentGate(ConsentOpts{Mode: permissions.New(permissions.Ask), Cwd: repo,
			Classifier: consentClassifier{v: Verdict{Level: Confirm, Network: true, Source: "llm"}},
			Sandboxed:  func(string) bool { return true },
			Confirm: func(_ context.Context, req ConsentRequest) ConsentAnswer {
				count := 0
				for _, need := range req.Needs {
					if need.Cap == CapUnconfined {
						count++
					}
				}
				if count != 1 {
					t.Fatalf("unconfined needs=%+v", req.Needs)
				}
				return ConsentAnswer{Allow: true, Answered: true, Scope: ScopeOnce, Needs: req.Needs, Exec: ExecUnconfined}
			}})
		if dec := gate(context.Background(), explicit); dec == nil || !dec.Direct || dec.ExecutionBinding == "" {
			t.Fatalf("explicit decision=%+v", dec)
		}
	})

	t.Run("enforce refuses impossible escape", func(t *testing.T) {
		gate := ConsentGate(ConsentOpts{Mode: permissions.New(permissions.Auto), Cwd: repo, Enforce: true,
			Classifier: consentClassifier{v: Verdict{Level: Confirm, Network: true}}, Sandboxed: func(string) bool { return true }})
		if dec := gate(context.Background(), call); dec == nil || !dec.Block {
			t.Fatalf("enforce decision = %+v", dec)
		}
	})
}
