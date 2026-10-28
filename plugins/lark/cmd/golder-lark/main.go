package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/BurntSushi/toml"
	"github.com/getan/golder/plugins/lark"
	"github.com/getan/golder/plugins/lark/internal/larkmcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "golder-lark:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	command := "serve"
	if len(args) > 0 && args[0] == "--version" {
		fmt.Fprintln(out, "golder-lark", version)
		return nil
	}
	if len(args) > 0 && args[0] != "--help" {
		command = args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet("golder-lark "+command, flag.ContinueOnError)
	flags.SetOutput(errOut)
	service := flags.String("service", "lark", "setup service: lark (existing application + user OAuth) or project")
	events := flags.Bool("events", false, "emit operator-only setup NDJSON")
	larkCLI := flags.String("lark-cli", "", "absolute installed lark-cli native binary (never a JS wrapper)")
	projectCLI := flags.String("project-cli", "", "absolute installed meegle native binary")
	skillsDir := flags.String("skills-dir", "", "absolute directory containing the optional lark Skill")
	dir := flags.String("dir", "", "install-assets destination; existing files are never overwritten")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(out, "Commands: serve | setup --events [--service=lark|project] | print-config | install-assets --dir <directory> | --version")
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	switch command {
	case "print-config":
		return printConfig(out, *larkCLI, *projectCLI, *skillsDir)
	case "install-assets":
		return installAssets(*dir, out)
	case "serve", "setup":
		config, err := larkmcp.FromEnv()
		if err != nil {
			return err
		}
		plugin := larkmcp.New(config, nil)
		if command == "setup" {
			if !*events {
				return errors.New("setup requires --events; let golder mcp setup manage the browser and private HOME")
			}
			return plugin.Setup(ctx, *service, out)
		}
		return plugin.Server(version).Run(ctx, &mcp.StdioTransport{})
	default:
		return errors.New("unknown command; use --help")
	}
}

func printConfig(out io.Writer, larkCLI, projectCLI, skills string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}
	if skills != "" && !filepath.IsAbs(skills) {
		return errors.New("skills-dir must be absolute")
	}
	dependencies := []string{}
	env := map[string]string{"LARK_MCP_IDENTITY": "user", "MEEGLE_HOST": "project.feishu.cn"}
	for key, path := range map[string]string{"LARK_MCP_CLI": larkCLI, "LARK_MCP_PROJECT_CLI": projectCLI} {
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			return errors.New("CLI paths must be absolute native binaries")
		}
		dependencies = append(dependencies, path)
		env[key] = path
	}
	source := map[string]any{
		"enabled": false, "transport": "stdio", "command": exe, "network": true, "state_dir": true, "call_timeout": "120s",
		"setup":           []string{exe, "setup", "--events", "--service=lark"},
		"setup_variants":  map[string][]string{"project": {exe, "setup", "--events", "--service=project"}},
		"setup_origins":   []string{"https://accounts.feishu.cn", "https://open.feishu.cn", "https://project.feishu.cn"},
		"confirm_tools":   []string{"doc_write", "message_send", "ticket_update", "ticket_transition", "sheet_write", "sheet_structure", "base_record_write", "cli_call"},
		"aggregate_tools": []string{"cli_call"}, "env": env,
	}
	if len(dependencies) > 0 {
		source["executables"] = dependencies
	}
	if skills != "" {
		source["skills"] = skills
	}
	return toml.NewEncoder(out).Encode(map[string]any{"mcp": map[string]any{"servers": map[string]any{"lark": source}}})
}

func installAssets(dir string, out io.Writer) error {
	if dir == "" || !filepath.IsAbs(dir) {
		return errors.New("install-assets requires an explicit absolute --dir")
	}
	if st, err := os.Lstat(dir); err == nil && (st.Mode()&os.ModeSymlink != 0 || !st.IsDir()) {
		return errors.New("asset destination must be a real directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return fs.WalkDir(lark.Assets, "skills", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(path))
		if entry.IsDir() {
			if err := os.Mkdir(target, 0o700); err != nil && !os.IsExist(err) {
				return err
			}
			st, err := os.Lstat(target)
			if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
				return errors.New("asset directory is not a real directory")
			}
			return nil
		}
		data, err := lark.Assets.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		_, err = fmt.Fprintln(out, "Installed", path)
		return err
	})
}
