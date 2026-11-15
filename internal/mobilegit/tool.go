// Package mobilegit provides a structured Git tool for the mobile host. Local
// operations are confined to the workspace; clone, fetch and push additionally
// require an explicit network grant on the tool-call context. It never shells
// out to a system git binary, which Android does not provide.
package mobilegit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/getan/golder/internal/agentcore"
)

const maxOutput = 48 << 10

// Config is host-owned. Root must be the selected workspace. Token is used only
// for HTTPS remotes and is never written to output.
type Config struct {
	Root        string
	AuthorName  string
	AuthorEmail string
	Token       string
}

// Tool implements one structured git action. It is deliberately not a generic
// command runner: every operation has a schema and root-bound target.
type Tool struct {
	root   string
	author object.Signature
	token  string
}

// New validates the workspace root. It does not require a repository yet.
func New(cfg Config) (*Tool, error) {
	if strings.TrimSpace(cfg.Root) == "" {
		return nil, errors.New("mobilegit: workspace root is required")
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, fmt.Errorf("mobilegit: workspace: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("mobilegit: workspace is not a directory")
	}
	return &Tool{
		root: root,
		author: object.Signature{
			Name: cfg.AuthorName, Email: cfg.AuthorEmail,
		},
		token: cfg.Token,
	}, nil
}

func (t *Tool) Name() string        { return "git" }
func (t *Tool) Description() string { return gitDescription }
func (t *Tool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}
func (t *Tool) Schema() json.RawMessage { return json.RawMessage(gitSchema) }

type args struct {
	Action  string `json:"action"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message,omitempty"`
	Name    string `json:"name,omitempty"`
	URL     string `json:"url,omitempty"`
	Remote  string `json:"remote,omitempty"`
	Branch  string `json:"branch,omitempty"`
	All     bool   `json:"all,omitempty"`
	Max     int    `json:"max,omitempty"`
	Depth   int    `json:"depth,omitempty"`
}

func (t *Tool) Execute(ctx context.Context, id string, raw json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	var a args
	if err := json.Unmarshal(raw, &a); err != nil {
		return errorResult("git: invalid arguments: " + err.Error()), nil
	}
	a.Action = strings.TrimSpace(a.Action)
	if a.Action == "" {
		return errorResult("git: action is required"), nil
	}
	out, err := t.run(ctx, a)
	if err != nil {
		return errorResult("git " + a.Action + ": " + err.Error()), nil
	}
	return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(limit(out))}}, nil
}

func (t *Tool) run(ctx context.Context, a args) (string, error) {
	switch a.Action {
	case "init":
		return t.init()
	case "clone":
		return t.clone(ctx, a)
	case "status":
		return t.status()
	case "log":
		return t.log(a)
	case "add":
		return t.add(a)
	case "commit":
		return t.commit(a)
	case "branch":
		return t.branch(a)
	case "checkout":
		return t.checkout(a)
	case "remote":
		return t.remote()
	case "fetch":
		return t.fetch(ctx, a)
	case "push":
		return t.push(ctx, a)
	case "diff":
		return t.diff(a)
	default:
		return "", fmt.Errorf("unsupported action %q", a.Action)
	}
}

func (t *Tool) repo() (*git.Repository, error) {
	repo, err := git.PlainOpen(t.root)
	if err != nil {
		return nil, fmt.Errorf("not a Git repository (use clone or init): %w", err)
	}
	return repo, nil
}

func (t *Tool) init() (string, error) {
	if _, err := git.PlainOpen(t.root); err == nil {
		return "Repository already initialized.", nil
	}
	if _, err := git.PlainInit(t.root, false); err != nil {
		return "", err
	}
	return "Initialized empty Git repository.", nil
}

func (t *Tool) status() (string, error) {
	repo, err := t.repo()
	if err != nil {
		return "", err
	}
	w, err := repo.Worktree()
	if err != nil {
		return "", err
	}
	st, err := w.Status()
	if err != nil {
		return "", err
	}
	head, headErr := repo.Head()
	var b strings.Builder
	if headErr == nil && head.Name().IsBranch() {
		fmt.Fprintf(&b, "branch: %s\n", head.Name().Short())
	} else {
		b.WriteString("branch: (no commits)\n")
	}
	if st.IsClean() {
		b.WriteString("working tree clean\n")
	} else {
		b.WriteString(st.String())
	}
	return b.String(), nil
}

func (t *Tool) log(a args) (string, error) {
	repo, err := t.repo()
	if err != nil {
		return "", err
	}
	head, err := repo.Head()
	if err != nil {
		return "", errors.New("repository has no commits")
	}
	max := a.Max
	if max <= 0 {
		max = 10
	}
	if max > 50 {
		max = 50
	}
	iter, err := repo.Log(&git.LogOptions{From: head.Hash(), Order: git.LogOrderCommitterTime})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	count := 0
	err = iter.ForEach(func(c *object.Commit) error {
		if count >= max {
			return nil
		}
		count++
		subject := strings.SplitN(strings.TrimSpace(c.Message), "\n", 2)[0]
		fmt.Fprintf(&b, "%s %s %s\n", c.Hash.String()[:8], c.Author.When.UTC().Format("2006-01-02"), subject)
		return nil
	})
	if err != nil {
		return "", err
	}
	if count == 0 {
		return "No commits.", nil
	}
	return b.String(), nil
}

func (t *Tool) add(a args) (string, error) {
	repo, err := t.repo()
	if err != nil {
		return "", err
	}
	w, err := repo.Worktree()
	if err != nil {
		return "", err
	}
	path, err := t.target(a.Path)
	if err != nil {
		return "", err
	}
	if a.All {
		if err := w.AddWithOptions(&git.AddOptions{All: true}); err != nil {
			return "", err
		}
		return "Staged all tracked changes.", nil
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", errors.New("path is required (or set all=true)")
	}
	rel, err := filepath.Rel(t.root, path)
	if err != nil {
		return "", err
	}
	if _, err := w.Add(rel); err != nil {
		return "", err
	}
	return "Staged " + rel + ".", nil
}

func (t *Tool) commit(a args) (string, error) {
	if strings.TrimSpace(a.Message) == "" {
		return "", errors.New("commit message is required")
	}
	if strings.ContainsRune(a.Message, '\x00') {
		return "", errors.New("commit message contains NUL")
	}
	repo, err := t.repo()
	if err != nil {
		return "", err
	}
	w, err := repo.Worktree()
	if err != nil {
		return "", err
	}
	st, err := w.Status()
	if err != nil {
		return "", err
	}
	if st.IsClean() {
		return "", errors.New("no staged changes to commit")
	}
	sig := t.author
	if sig.Name == "" || sig.Email == "" {
		if cfg, cfgErr := repo.Config(); cfgErr == nil {
			if sig.Name == "" {
				sig.Name = cfg.User.Name
			}
			if sig.Email == "" {
				sig.Email = cfg.User.Email
			}
		}
	}
	if sig.Name == "" {
		sig.Name = "Golder Mobile"
	}
	if sig.Email == "" {
		sig.Email = "golder@localhost"
	}
	sig.When = time.Now()
	hash, err := w.Commit(strings.TrimSpace(a.Message), &git.CommitOptions{All: a.All, Author: &sig, Committer: &sig})
	if err != nil {
		return "", err
	}
	return "Committed " + hash.String()[:8] + ".", nil
}

func (t *Tool) branch(a args) (string, error) {
	repo, err := t.repo()
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		head, _ := repo.Head()
		iter, err := repo.Branches()
		if err != nil {
			return "", err
		}
		var names []string
		_ = iter.ForEach(func(ref *plumbing.Reference) error {
			label := ref.Name().Short()
			if head != nil && ref.Name() == head.Name() {
				label += " *"
			}
			names = append(names, label)
			return nil
		})
		sort.Strings(names)
		if len(names) == 0 {
			return "No local branches.", nil
		}
		return strings.Join(names, "\n"), nil
	}
	if err := validateBranch(name); err != nil {
		return "", err
	}
	w, err := repo.Worktree()
	if err != nil {
		return "", err
	}
	exists := false
	if _, err := repo.Branch(name); err == nil {
		exists = true
	}
	opts := &git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(name), Create: !exists}
	if err := w.Checkout(opts); err != nil {
		return "", err
	}
	if exists {
		return "Switched to branch " + name + ".", nil
	}
	return "Created and switched to branch " + name + ".", nil
}

func (t *Tool) checkout(a args) (string, error) {
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return "", errors.New("branch name is required")
	}
	if err := validateBranch(name); err != nil {
		return "", err
	}
	repo, err := t.repo()
	if err != nil {
		return "", err
	}
	if _, err := repo.Branch(name); err != nil {
		return "", errors.New("branch does not exist")
	}
	w, err := repo.Worktree()
	if err != nil {
		return "", err
	}
	if err := w.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(name)}); err != nil {
		return "", err
	}
	return "Switched to branch " + name + ".", nil
}

func (t *Tool) remote() (string, error) {
	repo, err := t.repo()
	if err != nil {
		return "", err
	}
	remotes, err := repo.Remotes()
	if err != nil {
		return "", err
	}
	if len(remotes) == 0 {
		return "No remotes configured.", nil
	}
	var b strings.Builder
	for _, r := range remotes {
		fmt.Fprintf(&b, "%s %s\n", r.Config().Name, strings.Join(r.Config().URLs, ", "))
	}
	return b.String(), nil
}

func (t *Tool) clone(ctx context.Context, a args) (string, error) {
	if err := requireNetwork(ctx); err != nil {
		return "", err
	}
	url := strings.TrimSpace(a.URL)
	if url == "" {
		return "", errors.New("url is required")
	}
	target, err := t.target(a.Path)
	if err != nil {
		return "", err
	}
	if target == t.root {
		entries, err := os.ReadDir(t.root)
		if err != nil {
			return "", err
		}
		if len(entries) > 0 {
			return "", errors.New("workspace is not empty; choose a subdirectory")
		}
	}
	depth := a.Depth
	if depth < 0 {
		return "", errors.New("depth must not be negative")
	}
	opts := &git.CloneOptions{URL: url, Auth: t.auth(url), Depth: depth}
	if depth > 0 {
		opts.SingleBranch = true
	}
	if _, err := git.PlainCloneContext(ctx, target, false, opts); err != nil {
		return "", err
	}
	return "Cloned into " + strings.TrimPrefix(target, t.root+string(os.PathSeparator)) + ".", nil
}

func (t *Tool) fetch(ctx context.Context, a args) (string, error) {
	if err := requireNetwork(ctx); err != nil {
		return "", err
	}
	repo, err := t.repo()
	if err != nil {
		return "", err
	}
	remote := defaultRemote(a.Remote)
	err = repo.FetchContext(ctx, &git.FetchOptions{RemoteName: remote, Auth: t.remoteAuth(repo, remote)})
	if errors.Is(err, git.NoErrAlreadyUpToDate) {
		return "Already up to date.", nil
	}
	if err != nil {
		return "", err
	}
	return "Fetched " + remote + ".", nil
}

func (t *Tool) push(ctx context.Context, a args) (string, error) {
	if err := requireNetwork(ctx); err != nil {
		return "", err
	}
	repo, err := t.repo()
	if err != nil {
		return "", err
	}
	head, err := repo.Head()
	if err != nil || !head.Name().IsBranch() {
		return "", errors.New("HEAD is not a branch")
	}
	branch := strings.TrimSpace(a.Branch)
	if branch == "" {
		branch = head.Name().Short()
	}
	remote := defaultRemote(a.Remote)
	spec := config.RefSpec("refs/heads/" + branch + ":refs/heads/" + branch)
	err = repo.PushContext(ctx, &git.PushOptions{RemoteName: remote, RefSpecs: []config.RefSpec{spec}, Auth: t.remoteAuth(repo, remote)})
	if errors.Is(err, git.NoErrAlreadyUpToDate) {
		return "Already up to date.", nil
	}
	if err != nil {
		return "", err
	}
	return "Pushed " + branch + " to " + remote + ".", nil
}

func (t *Tool) diff(a args) (string, error) {
	repo, err := t.repo()
	if err != nil {
		return "", err
	}
	head, err := repo.Head()
	if err != nil {
		return "", errors.New("repository has no commits")
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return "", err
	}
	parents := commit.Parents()
	parent, err := parents.Next()
	if err != nil {
		return "Initial commit has no parent diff.", nil
	}
	patch, err := parent.Patch(commit)
	if err != nil {
		return "", err
	}
	return limit(patch.String()), nil
}

func (t *Tool) auth(url string) transport.AuthMethod {
	if !strings.HasPrefix(url, "https://") || t.token == "" {
		return nil
	}
	return &githttp.BasicAuth{Username: "x-access-token", Password: t.token}
}

func (t *Tool) remoteAuth(repo *git.Repository, name string) transport.AuthMethod {
	r, err := repo.Remote(name)
	if err != nil || r == nil {
		return nil
	}
	urls := r.Config().URLs
	if len(urls) == 0 {
		return nil
	}
	return t.auth(urls[0])
}

func (t *Tool) target(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || path == "." {
		return t.root, nil
	}
	if filepath.IsAbs(path) {
		return "", errors.New("path must be relative to the workspace")
	}
	abs := filepath.Clean(filepath.Join(t.root, path))
	rel, err := filepath.Rel(t.root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", errors.New("path escapes the workspace")
	}
	return abs, nil
}

func requireNetwork(ctx context.Context) error {
	if !agentcore.NetworkGrantedFromContext(ctx) {
		return errors.New("network access is not authorized for this call; the host must grant it")
	}
	return nil
}

func defaultRemote(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "origin"
	}
	return name
}

func validateBranch(name string) error {
	if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, " ~^:?*[\\") || strings.Contains(name, "..") {
		return errors.New("invalid branch name")
	}
	return nil
}

func limit(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	return s[:maxOutput] + "\n… output truncated"
}

func errorResult(text string) agentcore.AgentToolResult {
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(text)},
		IsError: true,
	}
}

const gitDescription = "Structured Git operations inside the workspace using a pure-Go implementation. " +
	"Actions: init, clone, status, log, diff (HEAD vs parent), add, commit, branch (list/create/switch), " +
	"checkout, remote, fetch, push. Paths are workspace-relative. clone/fetch/push require the host to " +
	"grant network access for this exact call; without it they fail closed. This is not a generic shell: " +
	"there is no arbitrary git subcommand, no hooks execution and no system git binary. " +
	"Commit uses the configured author, then repository config, then a local fallback identity."

const gitSchema = `{
  "type":"object",
  "properties":{
    "action":{"type":"string","enum":["init","clone","status","log","diff","add","commit","branch","checkout","remote","fetch","push"]},
    "path":{"type":"string","description":"Workspace-relative file or clone directory."},
    "message":{"type":"string","description":"Commit message."},
    "name":{"type":"string","description":"Branch name."},
    "url":{"type":"string","description":"HTTPS clone URL."},
    "remote":{"type":"string","description":"Remote name; defaults to origin."},
    "branch":{"type":"string","description":"Branch to push; defaults to current."},
    "all":{"type":"boolean","description":"For add/commit: include tracked modifications and deletions."},
    "max":{"type":"integer","minimum":1,"maximum":50,"description":"Log entries; defaults to 10."},
    "depth":{"type":"integer","description":"Clone depth; 0 means full history."}
  },
  "required":["action"],
  "additionalProperties":false
}`
