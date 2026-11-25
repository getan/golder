package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"syscall"

	"github.com/getan/golder/extension/plugins/docx/internal/docxmcp"
	"github.com/getan/golder/extension/plugins/docx/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "golder-docx:", err)
		os.Exit(1)
	}
}

func run(args []string, out, errOut io.Writer) error {
	command := "serve"
	if len(args) > 0 && args[0] == "--version" {
		fmt.Fprintln(out, "golder-docx", version.Version)
		return nil
	}
	if len(args) > 0 && args[0] != "--help" {
		command = args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet("golder-docx "+command, flag.ContinueOnError)
	flags.SetOutput(errOut)
	root := flags.String("root", "", "workspace root; defaults to the current directory")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(out, "Commands: serve [--root <dir>] | print-config | --version")
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	switch command {
	case "serve":
		// A soft ceiling makes a memory bomb fail the plugin process rather than
		// the host; the preflight bounds below are the primary defense.
		debug.SetMemoryLimit(512 << 20)
		plugin, err := docxmcp.New(*root)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return plugin.Server(version.Version).Run(ctx, &mcp.StdioTransport{})
	case "print-config":
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "[mcp.servers.docx]\n"+
			"enabled = false\n"+
			"transport = \"stdio\"\n"+
			"command = %s\n"+
			"args = [\"serve\"]\n"+
			"allowed_tools = [\"docx_read\", \"docx_create\", \"docx_edit\"]\n"+
			"call_timeout = \"30s\"\n"+
			"network = false\n", strconv.Quote(exe))
		return nil
	default:
		return errors.New("unknown command; use serve, print-config or --version")
	}
}
