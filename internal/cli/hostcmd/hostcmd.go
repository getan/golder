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
//
// Every line comes from hostcompat.Resolve, the same decision Open makes, so
// what this command says is what golder actually does: the host it names is the
// host a report goes to, and a host it calls unreachable is one no report
// reaches.
func runStatus(out, errOut io.Writer) int {
	getenv := os.Getenv
	res := hostcompat.Resolve(getenv)

	fmt.Fprintf(out, "golder %s, reporting as %q\n", Version, hostcompat.Harness)
	printHost(out, res)
	printHostVars(out, res)

	// A manifest is what makes an external tuios daemon recognise a golder pane
	// and read the prompts it draws. golder's own workspace and herdr learn the
	// agent from the report, so the manifest half is reported only where it
	// applies.
	switch res.Host {
	case hostcompat.MuxGolder:
		fmt.Fprintln(out, "manifest:    not needed — golder's own workspace reads the report itself.")
		return 0
	case hostcompat.MuxHerdr:
		fmt.Fprintln(out, "manifest:    not needed — herdr learns the agent from the report itself.")
		return 0
	case "":
		if res.Multiplexer == hostcompat.MuxGolder || res.Multiplexer == hostcompat.MuxHerdr {
			// A workspace or herdr pane with something missing: no manifest
			// would fix it, so pointing at one would send the person down the
			// wrong path.
			return 0
		}
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

// printHost says which host golder reports to, or why it reports to none. It is
// the one line a person reads first, so each case says what is wrong and what
// would fix it, and names the terminal golder found even when it cannot report
// to it.
func printHost(out io.Writer, res hostcompat.Resolution) {
	switch {
	case res.Off:
		fmt.Fprintf(out, "host:        %s, but reporting is OFF (%s=%s)\n",
			nameOr(res.Multiplexer, "none"), hostcompat.EnvHostReport, res.Forced)
		return
	case res.Host != "":
		fmt.Fprintf(out, "host:        %s\n", res.Host)
	case res.Multiplexer == hostcompat.MuxNone && res.Forced != "":
		// A host was forced from a pane that is not one: say so, and say what
		// the environment still lacks.
		fmt.Fprintf(out, "host:        none — %s=%s names %s, which this pane cannot reach.\n",
			hostcompat.EnvHostReport, res.Forced, nameOr(res.Candidate, res.Forced))
		if len(res.Missing) > 0 {
			fmt.Fprintf(out, "             %s %s not set.\n", strings.Join(res.Missing, " and "), plural(len(res.Missing)))
		}
		return
	case res.Multiplexer == hostcompat.MuxTmux || res.Multiplexer == hostcompat.MuxZellij || res.Multiplexer == hostcompat.MuxScreen:
		fmt.Fprintf(out, "host:        none — this is a %s pane, and golder has no driver for %s: nothing is reported.\n",
			res.Multiplexer, res.Multiplexer)
		return
	case len(res.Missing) > 0:
		fmt.Fprintf(out, "host:        none — this is a %s pane, but %s %s not set.\n",
			res.Multiplexer, strings.Join(res.Missing, " and "), plural(len(res.Missing)))
		return
	default:
		fmt.Fprintln(out, "host:        none — this is not a golder workspace, tuios or herdr pane, so nothing is reported.")
		fmt.Fprintf(out, "             Start golder inside a %s window, a %s pane or golder's own workspace to see it on the rail.\n",
			hostcompat.MuxTuios, hostcompat.MuxHerdr)
		return
	}
	if res.Forced != "" {
		fmt.Fprintf(out, "             forced by %s=%s\n", hostcompat.EnvHostReport, res.Forced)
	}
}

// printHostVars names the variables golder reads for the terminal it found.
// Naming them makes a silently missing one obvious, which is the usual reason
// nothing shows up.
func printHostVars(out io.Writer, res hostcompat.Resolution) {
	switch res.Candidate {
	case hostcompat.MuxGolder:
		printVar(out, "TUIOS_SOCKET")
		printVar(out, "TUIOS_SESSION")
		printVar(out, "TUIOS_PANE_ID")
		printVar(out, "TUIOS_PANE_TOKEN")
	case hostcompat.MuxTuios:
		printVar(out, "TUIOS_PANE_ID")
		printVar(out, "TUIOS_SOCKET")
		printVar(out, "TUIOS_PANE_TOKEN")
	case hostcompat.MuxHerdr:
		printVar(out, "HERDR_PANE_ID")
		printVar(out, "HERDR_BIN_PATH")
		printVar(out, "HERDR_SOCKET_PATH")
	}
}

// nameOr is name, or fallback when name is empty.
func nameOr(name, fallback string) string {
	if name == "" {
		return fallback
	}
	return name
}

// plural picks the verb for a count of missing variables.
func plural(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
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
