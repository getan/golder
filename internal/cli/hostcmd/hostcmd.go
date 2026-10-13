// Package hostcmd implements the `golder host-compat` subcommand family: what
// the terminal multiplexer around this process sees, and the one file that
// makes tuios recognise a golder pane at all.
//
//	golder host-compat status               # which host, which manifest, what is missing
//	golder host-compat install [--print]    # write tuios's harness manifest
//	golder host-compat uninstall            # remove it, and only golder's
//
// It dispatches early in cmd/golder, the same way the session and update
// subcommands do: it is a standalone action that never touches the
// interactive/headless flag surface, and it works outside a multiplexer (where
// it explains what would happen inside one).
package hostcmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/getan/golder/internal/hostcompat"
)

// Run executes a host-compat subcommand with args, writing output to out and
// errors to errOut. It returns a process exit code.
func Run(cmd string, args []string, out, errOut io.Writer) int {
	switch cmd {
	case "status":
		return runStatus(out, errOut)
	case "install":
		return runInstall(args, out, errOut)
	case "uninstall":
		return runUninstall(out, errOut)
	case "":
		fmt.Fprintln(errOut, usage)
		return 2
	default:
		fmt.Fprintf(errOut, "golder host-compat: unknown command %q (want status, install, or uninstall)\n", cmd)
		return 2
	}
}

const usage = "usage: golder host-compat status | install [--print] | uninstall"

// runStatus prints what the multiplexer around this process knows and what it
// would need to know more. It is the command a person runs when their rail
// does not show golder.
func runStatus(out, errOut io.Writer) int {
	getenv := os.Getenv
	detected := hostcompat.Detect(getenv)
	forced := strings.TrimSpace(getenv(hostcompat.EnvHostReport))

	fmt.Fprintf(out, "golder %s, reporting as %q\n", Version, hostcompat.Harness)
	switch {
	case detected == "" && forced == "":
		fmt.Fprintln(out, "host:        none — this is not a tuios or herdr pane, so nothing is reported.")
		fmt.Fprintln(out, "             Start golder inside a tuios window or a herdr pane to see it on the rail.")
	case detected == "" && forced != "":
		fmt.Fprintf(out, "host:        %s (forced by %s, though this pane looks like no host)\n",
			forced, hostcompat.EnvHostReport)
	case forced == "off" || forced == "none" || forced == "0" || forced == "false":
		fmt.Fprintf(out, "host:        %s, but reporting is OFF (%s=%s)\n", detected, hostcompat.EnvHostReport, forced)
	default:
		fmt.Fprintf(out, "host:        %s\n", detected)
		if forced != "" {
			fmt.Fprintf(out, "             forced by %s=%s\n", hostcompat.EnvHostReport, forced)
		}
	}

	// The pane and socket each host reports through. Naming them makes a
	// silently missing variable obvious, which is the usual reason nothing
	// shows up.
	switch {
	case detected == "tuios" || forced == "tuios":
		printVar(out, "TUIOS_PANE_ID")
		printVar(out, "TUIOS_SOCKET")
		printVar(out, "TUIOS_PANE_TOKEN")
	case detected == "herdr" || forced == "herdr":
		printVar(out, "HERDR_PANE_ID")
		printVar(out, "HERDR_BIN_PATH")
		printVar(out, "HERDR_SOCKET_PATH")
	}

	// tuios needs a manifest to recognise a golder pane and to read the prompts
	// it draws; herdr needs nothing at all. So the manifest half is reported
	// only where it applies.
	if detected == "herdr" {
		fmt.Fprintln(out, "manifest:    not needed — herdr learns the agent from the report itself.")
		return 0
	}
	state, path, err := hostcompat.ManifestStatus(getenv)
	if err != nil {
		fmt.Fprintf(errOut, "golder host-compat: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "manifest:    %s\n", state)
	if path != "" {
		fmt.Fprintf(out, "             %s\n", path)
	}
	switch state {
	case hostcompat.ManifestAbsent, hostcompat.ManifestOutdated:
		fmt.Fprintln(out, "             run 'golder host-compat install', then restart the daemon with 'tuios kill-server'.")
	case hostcompat.ManifestForeign:
		fmt.Fprintln(out, "             This file is not golder's, so golder leaves it alone.")
		fmt.Fprintln(out, "             Move it aside to install golder's, or keep it and report state only.")
	}
	return 0
}

// printVar reports one environment variable, saying plainly when it is missing:
// an empty value is the usual reason nothing reaches the host.
func printVar(out io.Writer, name string) {
	if v := os.Getenv(name); v != "" {
		fmt.Fprintf(out, "%-12s %s=%s\n", "", name, v)
		return
	}
	fmt.Fprintf(out, "%-12s %s is not set\n", "", name)
}

// runInstall writes tuios's harness manifest, or prints it with --print so a
// person can install it themselves.
func runInstall(args []string, out, errOut io.Writer) int {
	print := false
	for _, a := range args {
		switch a {
		case "--print":
			print = true
		case "-h", "--help":
			fmt.Fprintln(out, usage)
			return 0
		default:
			fmt.Fprintf(errOut, "golder host-compat install: unknown flag %q\n%s\n", a, usage)
			return 2
		}
	}
	if print {
		_, _ = io.WriteString(out, hostcompat.ManifestSource())
		return 0
	}
	res, err := hostcompat.Install(os.Getenv)
	if err != nil {
		fmt.Fprintf(errOut, "golder host-compat install: %v\n", err)
		return 1
	}
	switch {
	case res.Changed:
		fmt.Fprintf(out, "installed %s\n", res.Path)
	default:
		fmt.Fprintf(out, "already installed and current in %s\n", res.Path)
	}
	// The daemon reads manifests when it starts, so a fresh install needs a
	// restart to take effect — and the restart closes every pane. Say both, and
	// leave the choice to the person.
	fmt.Fprintln(out, "tuios reads manifests at daemon start: run 'tuios kill-server' to pick it up (this closes every pane).")
	return 0
}

// runUninstall removes the manifest golder wrote, and only that one.
func runUninstall(out, errOut io.Writer) int {
	path, removed, err := hostcompat.Uninstall(os.Getenv)
	if err != nil {
		fmt.Fprintf(errOut, "golder host-compat uninstall: %v\n", err)
		return 1
	}
	switch {
	case removed:
		fmt.Fprintf(out, "removed %s\n", path)
	default:
		fmt.Fprintf(out, "nothing to remove: %s is not golder's manifest\n", path)
	}
	return 0
}

// Version is the golder build version, injected by cmd/golder so the status
// report names the binary a person is actually running.
var Version = "dev"
