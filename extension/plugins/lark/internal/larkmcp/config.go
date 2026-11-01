// Package larkmcp is the host-independent Lark/Project MCP adapter. Nothing in
// this package knows about Golder's UI, agent loop or authorization decisions.
package larkmcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/getan/golder/extension"
)

type Config struct {
	LarkCLI     string
	ProjectCLI  string
	Home        string
	ProjectHost string
	Profile     string
	AppID       string
	AppSecret   string
	Domains     string
}

func FromEnv() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	if explicit := os.Getenv("LARK_MCP_STATE_HOME"); explicit != "" {
		if !filepath.IsAbs(explicit) || filepath.Clean(explicit) == filepath.Clean(home) {
			return Config{}, errors.New("LARK_MCP_STATE_HOME must be a dedicated absolute directory, not your real HOME")
		}
		home = explicit
	} else if filepath.Base(home) != "home" || filepath.Base(filepath.Dir(filepath.Dir(home))) != "extensions" || !(strings.HasPrefix(filepath.Base(filepath.Dir(home)), "mcp-") || strings.HasPrefix(filepath.Base(filepath.Dir(home)), "plugin-")) {
		return Config{}, errors.New("golder-lark requires a private HOME: enable state_dir, or explicitly set LARK_MCP_STATE_HOME to a dedicated directory")
	}
	if identity := os.Getenv("LARK_MCP_IDENTITY"); identity != "" && identity != "user" {
		return Config{}, errors.New("this plugin requires user identity; bot/auto fallback is disabled")
	}
	c := Config{LarkCLI: os.Getenv("LARK_MCP_CLI"), ProjectCLI: os.Getenv("LARK_MCP_PROJECT_CLI"), Home: home,
		ProjectHost: os.Getenv("MEEGLE_HOST"), AppID: os.Getenv("LARK_MCP_APP_ID"), AppSecret: os.Getenv("LARK_MCP_APP_SECRET"), Domains: os.Getenv("LARK_MCP_DOMAINS")}
	if c.ProjectHost == "" {
		c.ProjectHost = "project.feishu.cn"
	}
	if c.Domains == "" {
		c.Domains = "docs,drive,im,sheets,base"
	}
	u, err := url.Parse("https://" + c.ProjectHost)
	if err != nil || u.Host != c.ProjectHost || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return Config{}, errors.New("MEEGLE_HOST must be a reviewed HTTPS hostname, not a URL")
	}
	for _, p := range []string{c.LarkCLI, c.ProjectCLI} {
		if p != "" && !filepath.IsAbs(p) {
			return Config{}, errors.New("CLI paths must be absolute native binaries; the plugin never searches PATH or downloads a wrapper")
		}
	}
	if raw, present := os.LookupEnv(extension.ExecutablePathsEnv); present {
		var paths []string
		if json.Unmarshal([]byte(raw), &paths) != nil {
			return Config{}, errors.New("invalid host executable declaration")
		}
		for _, p := range []string{c.LarkCLI, c.ProjectCLI} {
			if p == "" {
				continue
			}
			if resolved, e := filepath.EvalSymlinks(p); e == nil {
				p = resolved
			}
			admitted := false
			for _, declared := range paths {
				if declared == p {
					admitted = true
				}
			}
			if !admitted {
				return Config{}, errors.New("CLI path is not in the host-reviewed executables declaration; update the source and reapprove it")
			}
		}
	}
	sum := sha256.Sum256([]byte(home))
	c.Profile = "golder-" + hex.EncodeToString(sum[:8])
	return c, nil
}
