// Package mobilebuild implements the host-side GitHub Actions build tool.
// The model only asks for a build; tokens, workflow polling, artifact download
// and APK extraction stay inside the host.
package mobilebuild

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/getan/golder/internal/agentcore"
	git "github.com/go-git/go-git/v5"
)

const (
	workflowFile = "android.yml"
	defaultAPI   = "https://api.github.com"
	buildDir     = "golder-build"
)

type Config struct {
	Root    string
	Token   string
	APIBase string
}

type Tool struct {
	root    string
	token   string
	apiBase string
}

func New(cfg Config) (*Tool, error) {
	root, err := filepath.Abs(strings.TrimSpace(cfg.Root))
	if err != nil || strings.TrimSpace(cfg.Root) == "" {
		return nil, errors.New("mobilebuild: workspace root is required")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, errors.New("mobilebuild: workspace is not a directory")
	}
	apiBase := strings.TrimRight(strings.TrimSpace(cfg.APIBase), "/")
	if apiBase == "" {
		apiBase = defaultAPI
	}
	return &Tool{root: root, token: cfg.Token, apiBase: apiBase}, nil
}

func (t *Tool) Name() string { return "android_build" }
func (t *Tool) Description() string {
	return "Build the current Android project with GitHub Actions. The host triggers .github/workflows/android.yml, polls the run, downloads the APK, and returns APK_PATH. token/API details are host-owned."
}
func (t *Tool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}
func (t *Tool) Schema() json.RawMessage { return json.RawMessage(schema) }

type args struct {
	Branch  string `json:"branch,omitempty"`
	Variant string `json:"variant,omitempty"`
}

func (t *Tool) Execute(ctx context.Context, id string, raw json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	if !agentcore.NetworkGrantedFromContext(ctx) {
		return errorResult("android_build: network access is not authorized for this call"), nil
	}
	var a args
	if len(raw) > 0 && json.Unmarshal(raw, &a) != nil {
		return errorResult("android_build: invalid arguments"), nil
	}
	out, err := t.run(ctx, a)
	if err != nil {
		return errorResult("android_build: " + err.Error()), nil
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(out)},
	}, nil
}

func (t *Tool) run(ctx context.Context, a args) (string, error) {
	if strings.TrimSpace(t.token) == "" {
		return "", errors.New("GitHub token is not configured; add a PAT with Actions and Contents access")
	}
	owner, repo, branch, err := githubRepo(t.root)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Branch) != "" {
		branch = strings.TrimSpace(a.Branch)
	}
	workflowPath := filepath.Join(t.root, ".github", "workflows", workflowFile)
	if _, err := os.Stat(workflowPath); err != nil {
		if writeErr := os.MkdirAll(filepath.Dir(workflowPath), 0o755); writeErr != nil {
			return "", writeErr
		}
		if writeErr := os.WriteFile(workflowPath, []byte(workflowTemplate), 0o644); writeErr != nil {
			return "", writeErr
		}
		return "", errors.New("created .github/workflows/android.yml; commit and push it, then call android_build again")
	}

	if err := t.dispatch(ctx, owner, repo, branch); err != nil {
		return "", err
	}
	run, err := t.waitForRun(ctx, owner, repo, branch)
	if err != nil {
		return "", err
	}
	if run.Conclusion != "success" {
		summary := t.failureSummary(ctx, owner, repo, run)
		return "", fmt.Errorf("workflow %s: %s\n%s", run.Conclusion, run.HTMLURL, summary)
	}
	apk, err := t.downloadAPK(ctx, owner, repo, run.ID)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("GitHub Actions build succeeded.\nAPK_PATH=%s\nWorkflow: %s", apk, run.HTMLURL), nil
}

func (t *Tool) dispatch(ctx context.Context, owner, repo, branch string) error {
	payload, _ := json.Marshal(map[string]string{"ref": branch})
	path := fmt.Sprintf("/repos/%s/%s/actions/workflows/%s/dispatches", owner, repo, workflowFile)
	return t.api(ctx, http.MethodPost, path, payload, nil, http.StatusNoContent)
}

type workflowRun struct {
	ID         int64     `json:"id"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	HTMLURL    string    `json:"html_url"`
	HeadBranch string    `json:"head_branch"`
	Event      string    `json:"event"`
	CreatedAt  time.Time `json:"created_at"`
}

type runsResponse struct {
	WorkflowRuns []workflowRun `json:"workflow_runs"`
}

func (t *Tool) waitForRun(ctx context.Context, owner, repo, branch string) (workflowRun, error) {
	started := time.Now().Add(-10 * time.Second)
	deadline := time.Now().Add(10 * time.Minute)
	path := fmt.Sprintf("/repos/%s/%s/actions/workflows/%s/runs?event=workflow_dispatch&branch=%s&per_page=10",
		owner, repo, workflowFile, url.QueryEscape(branch))
	var run workflowRun
	found := false
	for !found && time.Now().Before(deadline) {
		var list runsResponse
		if err := t.api(ctx, http.MethodGet, path, nil, &list, http.StatusOK); err != nil {
			return run, err
		}
		for _, candidate := range list.WorkflowRuns {
			if candidate.HeadBranch == branch && candidate.CreatedAt.After(started) {
				run, found = candidate, true
				break
			}
		}
		if !found {
			if err := sleep(ctx, 4*time.Second); err != nil {
				return run, err
			}
		}
	}
	if !found {
		return run, errors.New("timed out waiting for the workflow run to appear")
	}
	for run.Status != "completed" && time.Now().Before(deadline) {
		if err := sleep(ctx, 5*time.Second); err != nil {
			return run, err
		}
		if err := t.api(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/actions/runs/%d", owner, repo, run.ID), nil, &run, http.StatusOK); err != nil {
			return run, err
		}
	}
	if run.Status != "completed" {
		return run, errors.New("timed out waiting for the workflow to finish")
	}
	return run, nil
}

func (t *Tool) failureSummary(ctx context.Context, owner, repo string, run workflowRun) string {
	var jobs struct {
		Jobs []struct {
			Name       string `json:"name"`
			Conclusion string `json:"conclusion"`
			Steps      []struct {
				Name       string `json:"name"`
				Conclusion string `json:"conclusion"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	_ = t.api(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs?per_page=20", owner, repo, run.ID), nil, &jobs, http.StatusOK)
	var b strings.Builder
	for _, job := range jobs.Jobs {
		if job.Conclusion == "success" || job.Conclusion == "" {
			continue
		}
		fmt.Fprintf(&b, "job %s: %s\n", job.Name, job.Conclusion)
		for _, step := range job.Steps {
			if step.Conclusion != "" && step.Conclusion != "success" {
				fmt.Fprintf(&b, "  step %s: %s\n", step.Name, step.Conclusion)
			}
		}
	}
	if b.Len() == 0 {
		return "workflow failed; inspect the run for details"
	}
	return b.String()
}

type artifact struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	ArchiveDownloadURL string `json:"archive_download_url"`
	Expired            bool   `json:"expired"`
}

func (t *Tool) downloadAPK(ctx context.Context, owner, repo string, runID int64) (string, error) {
	var list struct {
		Artifacts []artifact `json:"artifacts"`
	}
	if err := t.api(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/actions/runs/%d/artifacts", owner, repo, runID), nil, &list, http.StatusOK); err != nil {
		return "", err
	}
	var chosen *artifact
	for i := range list.Artifacts {
		a := &list.Artifacts[i]
		if !a.Expired && (a.Name == "golder-apk" || strings.Contains(a.Name, "apk")) {
			chosen = a
			break
		}
	}
	if chosen == nil {
		return "", errors.New("workflow succeeded but no APK artifact was found")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, chosen.ArchiveDownloadURL, nil)
	if err != nil {
		return "", err
	}
	t.auth(req)
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return "", fmt.Errorf("download artifact: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download artifact: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return "", err
	}
	outDir := filepath.Join(t.root, buildDir)
	if err := os.RemoveAll(outDir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return "", err
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", err
	}
	var apkPath string
	for _, f := range zr.File {
		if !strings.HasSuffix(strings.ToLower(f.Name), ".apk") {
			continue
		}
		name := filepath.Base(f.Name)
		dst := filepath.Join(outDir, name)
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		err = writeLimited(dst, rc, 128<<20)
		_ = rc.Close()
		if err != nil {
			return "", err
		}
		apkPath = dst
		break
	}
	if apkPath == "" {
		return "", errors.New("artifact contained no .apk")
	}
	return apkPath, nil
}

func (t *Tool) api(ctx context.Context, method, path string, body []byte, out any, want int) error {
	req, err := http.NewRequestWithContext(ctx, method, t.apiBase+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	t.auth(req)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 45 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != want {
		return fmt.Errorf("GitHub API %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tool) auth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+t.token)
}

func githubRepo(root string) (owner, repo, branch string, err error) {
	r, err := git.PlainOpen(root)
	if err != nil {
		return "", "", "", fmt.Errorf("open repository: %w", err)
	}
	head, err := r.Head()
	if err == nil && head.Name().IsBranch() {
		branch = head.Name().Short()
	}
	remotes, err := r.Remotes()
	if err != nil || len(remotes) == 0 {
		return "", "", branch, errors.New("Git remote is not configured")
	}
	for _, remote := range remotes {
		for _, raw := range remote.Config().URLs {
			if o, n, ok := parseGitHubURL(raw); ok {
				if branch == "" {
					branch = "main"
				}
				return o, n, branch, nil
			}
		}
	}
	return "", "", branch, errors.New("Git remote is not a GitHub HTTPS/SSH URL")
}

func parseGitHubURL(raw string) (owner, repo string, ok bool) {
	raw = strings.TrimSpace(raw)
	var path string
	switch {
	case strings.HasPrefix(raw, "git@github.com:"):
		path = strings.TrimPrefix(raw, "git@github.com:")
	case strings.HasPrefix(raw, "ssh://git@github.com/"):
		path = strings.TrimPrefix(raw, "ssh://git@github.com/")
	case strings.HasPrefix(raw, "https://github.com/"):
		path = strings.TrimPrefix(raw, "https://github.com/")
	default:
		return "", "", false
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func writeLimited(path string, r io.Reader, limit int64) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, io.LimitReader(r, limit))
	return err
}

func errorResult(text string) agentcore.AgentToolResult {
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(text)},
		IsError: true,
	}
}

const schema = `{
  "type":"object",
  "properties":{
    "branch":{"type":"string","description":"Git branch to build; defaults to current branch."},
    "variant":{"type":"string","enum":["debug"],"description":"Build variant. Currently debug."}
  },
  "additionalProperties":false
}`

const workflowTemplate = `name: android-build
on:
  workflow_dispatch:
  push:
    branches: [main]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-java@v4
        with:
          distribution: temurin
          java-version: "21"
          cache: gradle
      - uses: gradle/actions/setup-gradle@v4
      - run: chmod +x gradlew && ./gradlew assembleDebug
      - uses: actions/upload-artifact@v4
        with:
          name: golder-apk
          path: app/build/outputs/apk/debug/*.apk
`
