package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestConfigGenerationIsDisabledAndUsesUserIdentity(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"print-config", "--lark-cli=/native/lark-cli", "--skills-dir=/package/skills"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		MCP struct {
			Servers map[string]struct {
				Enabled        bool
				StateDir       bool `toml:"state_dir"`
				Env            map[string]string
				ConfirmTools   []string            `toml:"confirm_tools"`
				SetupVariants  map[string][]string `toml:"setup_variants"`
				AggregateTools []string            `toml:"aggregate_tools"`
			}
		}
	}
	if err := toml.Unmarshal(out.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	s := cfg.MCP.Servers["lark"]
	if s.Enabled || !s.StateDir || s.Env["LARK_MCP_IDENTITY"] != "user" || len(s.ConfirmTools) != 8 || len(s.SetupVariants["project"]) == 0 || len(s.AggregateTools) != 1 {
		t.Fatalf("unsafe registration defaults: %+v", s)
	}
}

func TestAssetsAreExplicitAndNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	if err := installAssets(dir, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "skills", "lark", "SKILL.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if err := installAssets(dir, &bytes.Buffer{}); err == nil {
		t.Fatal("asset reinstall silently overwrote reviewed instructions")
	}
}
