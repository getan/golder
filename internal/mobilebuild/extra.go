package mobilebuild

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/getan/golder/internal/agentcore"
	git "github.com/go-git/go-git/v5"
)

func headSHA(root string) (string, error) {
	r, err := git.PlainOpen(root)
	if err != nil {
		return "", err
	}
	head, err := r.Head()
	if err != nil {
		return "", err
	}
	return head.Hash().String(), nil
}

// reusableRun finds a successful run for the exact commit. This avoids
// re-running CI when the user only wants the previously built APK.
func (t *Tool) reusableRun(ctx context.Context, owner, repo, branch, sha string) (workflowRun, bool) {
	path := fmt.Sprintf(
		"/repos/%s/%s/actions/workflows/%s/runs?branch=%s&per_page=20",
		owner, repo, workflowFile, url.QueryEscape(branch),
	)
	var list runsResponse
	if err := t.api(ctx, "GET", path, nil, &list, 200); err != nil {
		return workflowRun{}, false
	}
	for _, run := range list.WorkflowRuns {
		if run.Conclusion == "success" && run.HeadSHA == sha {
			return run, true
		}
	}
	return workflowRun{}, false
}

// DownloadTool downloads the latest successful APK artifact without starting a
// new workflow run.
type DownloadTool struct {
	build *Tool
}

func NewDownload(cfg Config) (*DownloadTool, error) {
	build, err := New(cfg)
	if err != nil {
		return nil, err
	}
	return &DownloadTool{build: build}, nil
}

func (t *DownloadTool) Name() string { return "android_download" }
func (t *DownloadTool) Description() string {
	return "Download the APK from the latest successful GitHub Actions run without starting a new build. Use this after a build or when only the artifact is needed."
}
func (t *DownloadTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}
func (t *DownloadTool) Schema() json.RawMessage { return json.RawMessage(downloadSchema) }

type downloadArgs struct {
	RunID  int64  `json:"runId,omitempty"`
	Branch string `json:"branch,omitempty"`
}

func (t *DownloadTool) Execute(ctx context.Context, _ string, raw json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	if !agentcore.NetworkGrantedFromContext(ctx) {
		return errorResult("android_download: network access is not authorized for this call"), nil
	}
	var a downloadArgs
	if len(raw) > 0 && json.Unmarshal(raw, &a) != nil {
		return errorResult("android_download: invalid arguments"), nil
	}
	owner, repo, branch, err := githubRepo(t.build.root)
	if err != nil {
		return errorResult("android_download: " + err.Error()), nil
	}
	if strings.TrimSpace(a.Branch) != "" {
		branch = strings.TrimSpace(a.Branch)
	}
	run, err := t.successfulRun(ctx, owner, repo, branch, a.RunID)
	if err != nil {
		return errorResult("android_download: " + err.Error()), nil
	}
	apk, err := t.build.downloadAPK(ctx, owner, repo, run.ID)
	if err != nil {
		return errorResult("android_download: " + err.Error()), nil
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(fmt.Sprintf(
			"Downloaded the latest successful APK; no build was started.\nAPK_PATH=%s\nWorkflow: %s",
			apk, run.HTMLURL,
		))},
	}, nil
}

func (t *DownloadTool) successfulRun(ctx context.Context, owner, repo, branch string, runID int64) (workflowRun, error) {
	var run workflowRun
	if runID > 0 {
		path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d", owner, repo, runID)
		if err := t.build.api(ctx, "GET", path, nil, &run, 200); err != nil {
			return run, err
		}
		if run.Conclusion != "success" {
			return run, fmt.Errorf("run %d is %s, not successful", runID, run.Conclusion)
		}
		return run, nil
	}
	path := fmt.Sprintf(
		"/repos/%s/%s/actions/workflows/%s/runs?branch=%s&per_page=20",
		owner, repo, workflowFile, url.QueryEscape(branch),
	)
	var list runsResponse
	if err := t.build.api(ctx, "GET", path, nil, &list, 200); err != nil {
		return run, err
	}
	for _, candidate := range list.WorkflowRuns {
		if candidate.Conclusion == "success" {
			return candidate, nil
		}
	}
	return run, errors.New("no successful workflow run was found")
}

// InstallTool opens the host installer for an APK that is already on disk.
// It never builds or downloads anything.
type InstallTool struct {
	root string
}

func NewInstall(cfg Config) (*InstallTool, error) {
	root, err := filepath.Abs(strings.TrimSpace(cfg.Root))
	if err != nil || strings.TrimSpace(cfg.Root) == "" {
		return nil, errors.New("mobilebuild: workspace root is required")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, errors.New("mobilebuild: workspace is not a directory")
	}
	return &InstallTool{root: root}, nil
}

func (t *InstallTool) Name() string { return "android_install" }
func (t *InstallTool) Description() string {
	return "Ask the Android host to open the system package installer for an APK already in the workspace. Prefer the transcript's Install APK button when it is available; use this tool only when the user explicitly asks the agent to trigger installation. No build or download is performed."
}
func (t *InstallTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}
func (t *InstallTool) Schema() json.RawMessage { return json.RawMessage(installSchema) }

type installArgs struct {
	Path string `json:"path,omitempty"`
}

func (t *InstallTool) Execute(_ context.Context, _ string, raw json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	var a installArgs
	if len(raw) > 0 && json.Unmarshal(raw, &a) != nil {
		return errorResult("android_install: invalid arguments"), nil
	}
	path := strings.TrimSpace(a.Path)
	if path == "" {
		var err error
		path, err = latestAPK(t.root)
		if err != nil {
			return errorResult("android_install: " + err.Error()), nil
		}
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(t.root, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return errorResult("android_install: " + err.Error()), nil
	}
	rel, err := filepath.Rel(t.root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errorResult("android_install: APK must be inside the workspace"), nil
	}
	if !strings.EqualFold(filepath.Ext(abs), ".apk") {
		return errorResult("android_install: expected an .apk file"), nil
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return errorResult("android_install: APK does not exist"), nil
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(fmt.Sprintf(
			"INSTALL_APK=%s\nAPK is ready; the host will open the Android package installer.",
			abs,
		))},
	}, nil
}

func latestAPK(root string) (string, error) {
	dir := filepath.Join(root, buildDir)
	var newest string
	var newestTime time.Time
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".apk") {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		if newest == "" || info.ModTime().After(newestTime) {
			newest, newestTime = path, info.ModTime()
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if newest == "" {
		return "", errors.New("no downloaded APK was found; run android_build or android_download first")
	}
	return newest, nil
}

const downloadSchema = `{
  "type":"object",
  "properties":{
    "runId":{"type":"integer","description":"Optional GitHub Actions run id. Defaults to the latest successful run."},
    "branch":{"type":"string","description":"Git branch; defaults to current branch."}
  },
  "additionalProperties":false
}`

const installSchema = `{
  "type":"object",
  "properties":{
    "path":{"type":"string","description":"APK path inside the workspace. Defaults to the newest APK under golder-build/."}
  },
  "additionalProperties":false
}`
