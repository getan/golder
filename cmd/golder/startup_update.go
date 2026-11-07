package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"

	"github.com/getan/golder/internal/selfupdate"
	"github.com/getan/golder/internal/terminalstate"
)

const startupUpdateWait = 800 * time.Millisecond

// startupUpdateAllowed is deliberately stricter than the TUI selection: a
// redirected stdin, CI job, or a caller-supplied writer must never be prompted.
func startupUpdateAllowed(opts cliOptions, out io.Writer) bool {
	file, ok := out.(*os.File)
	ci := strings.TrimSpace(strings.ToLower(os.Getenv("CI")))
	disabled := strings.TrimSpace(strings.ToLower(os.Getenv("GOLDER_NO_UPDATE_CHECK")))
	return startupUpdateMode(opts, ci, disabled) && ok && file == os.Stdout && term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(file.Fd())
}

func startupUpdateMode(opts cliOptions, ci, disabled string) bool {
	return !opts.noUpdateCheck && opts.prompt == "" && !opts.printProvided && (opts.outputFmt == "" || opts.outputFmt == "text") && !opts.subagentRPC && !opts.dream && !opts.githubReview && !opts.listSessions &&
		(ci == "" || ci == "false" || ci == "0") && disabled != "true" && disabled != "1"
}

type updateChoice int

const (
	choiceUpdate updateChoice = iota
	choiceSkipOnce
	choiceSkipVersion
	choiceInterrupt
)

type updatePrompt struct {
	heading string
	options []string
	index   int
	choice  updateChoice
	cancel  updateChoice
	done    bool
}

func (m updatePrompt) Init() tea.Cmd { return nil }

func (m updatePrompt) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "up", "k":
		m.index = (m.index + len(m.options) - 1) % len(m.options)
	case "down", "j":
		m.index = (m.index + 1) % len(m.options)
	case "1", "2", "3":
		index := int(key.String()[0] - '1')
		if index < len(m.options) {
			m.index = index
		}
	case "enter":
		m.choice, m.done = updateChoice(m.index), true
		return m, tea.Quit
	case "esc":
		m.choice, m.done = m.cancel, true
		return m, tea.Quit
	case "ctrl+c":
		m.choice, m.done = choiceInterrupt, true
		return m, tea.Quit
	}
	return m, nil
}

func (m updatePrompt) View() tea.View {
	var text strings.Builder
	fmt.Fprintf(&text, "\n  %s\n\n", m.heading)
	for i, option := range m.options {
		marker := " "
		if i == m.index {
			marker = "›"
		}
		fmt.Fprintf(&text, "  %s %d. %s\n", marker, i+1, option)
	}
	text.WriteString("\n  ↑/↓ select · Enter confirm · Esc skip · Ctrl+C quit\n")
	return tea.NewView(text.String())
}

func chooseUpdate(in io.Reader, out io.Writer, heading string, options []string, cancel updateChoice) (choice updateChoice, err error) {
	input, _ := in.(*os.File)
	output, _ := out.(*os.File)
	guard, err := terminalstate.Capture(input, output)
	if err != nil {
		return cancel, err
	}
	defer func() {
		if restoreErr := guard.Restore(); restoreErr != nil && err == nil {
			err = restoreErr
		}
	}()
	model, err := tea.NewProgram(updatePrompt{heading: heading, options: options, cancel: cancel}, tea.WithInput(in), tea.WithOutput(out)).Run()
	if err != nil {
		return choiceSkipOnce, err
	}
	result, ok := model.(updatePrompt)
	if !ok || !result.done {
		return choiceSkipOnce, nil
	}
	return result.choice, nil
}

type installKind int

const (
	installStandalone installKind = iota
	installHomebrew
	installManaged
)

// classifyInstall uses the resolved executable, not the shell's PATH: updating
// some other installation can produce a misleading success and update loop.
func classifyInstall(path string) installKind {
	clean := filepath.Clean(path)
	p := "/" + strings.TrimPrefix(strings.ToLower(filepath.ToSlash(clean)), "/")
	if strings.Contains(p, "/cellar/golder/") {
		return installHomebrew
	}
	// A running Windows executable cannot be safely replaced this way. Unknown
	// locations may be package-managed and must not be overwritten speculatively.
	if runtime.GOOS == "windows" {
		return installManaged
	}
	if strings.Contains(p, "/cellar/") || strings.HasPrefix(p, "/usr/bin/") || strings.HasPrefix(p, "/nix/store/") || strings.HasPrefix(p, "/snap/") || strings.HasPrefix(p, "/opt/") {
		return installManaged
	}
	if clean == "/usr/local/bin/golder" {
		return installStandalone
	}
	if home, err := os.UserHomeDir(); err == nil && clean == filepath.Join(home, ".local", "bin", "golder") {
		return installStandalone
	}
	if dir := os.Getenv("GOLDER_INSTALL_DIR"); dir != "" && filepath.IsAbs(dir) && clean == filepath.Join(dir, "golder") {
		return installStandalone
	}
	return installManaged
}

type startupUpdateServices struct {
	latest  func(string, time.Duration) string
	offer   func(string, string) bool
	updater func() (*selfupdate.Updater, error)
	choose  func(io.Reader, io.Writer, string, []string, updateChoice) (updateChoice, error)
	skip    func(string) error
	install func(context.Context, *selfupdate.Updater, installKind, string, io.Reader, io.Writer, io.Writer) error
}

func startupUpdate(ctx context.Context, current string, in io.Reader, out, errOut io.Writer) (bool, int) {
	return runStartupUpdate(ctx, current, in, out, errOut, startupUpdateServices{
		latest: selfupdate.LatestForStartup, offer: selfupdate.StartupOffer,
		updater: selfupdate.NewUpdater, choose: chooseUpdate, skip: selfupdate.SkipVersion,
		install: installStartupUpdate,
	})
}

func runStartupUpdate(ctx context.Context, current string, in io.Reader, out, errOut io.Writer, services startupUpdateServices) (bool, int) {
	latest := services.latest(current, startupUpdateWait)
	if !services.offer(current, latest) {
		return false, 0
	}
	u, err := services.updater()
	if err != nil {
		fmt.Fprintf(errOut, "golder: cannot locate executable for update: %v\n", err)
		return false, 0
	}
	kind := classifyInstall(u.ExecPath)
	updateLabel := "Update now"
	if kind == installHomebrew {
		updateLabel = "Update with Homebrew (brew upgrade golder)"
	} else if kind == installManaged {
		updateLabel = "Update using your installation method"
	}
	heading := fmt.Sprintf("Golder update available: %s → %s\n\n  Release notes: https://github.com/%s/releases/tag/%s", current, latest, selfupdate.Repo, latest)
	choice, err := services.choose(in, out, heading, []string{updateLabel, "Skip this time", "Skip this version"}, choiceSkipOnce)
	if err != nil {
		fmt.Fprintf(errOut, "golder: update prompt unavailable: %v\n", err)
		return false, 0
	}
	switch choice {
	case choiceInterrupt:
		return true, 130
	case choiceSkipVersion:
		if err := services.skip(latest); err != nil {
			fmt.Fprintf(errOut, "golder: could not remember skipped version (skipping this time): %v\n", err)
		}
		return false, 0
	case choiceSkipOnce:
		return false, 0
	}
	if kind == installManaged {
		fmt.Fprintf(out, "\n  Golder cannot safely replace %s automatically. Update it using your installation method, then start Golder again.\n", u.ExecPath)
		return false, 0
	}
	fmt.Fprintf(out, "\nUpdating Golder %s → %s…\n", current, latest)
	err = services.install(ctx, u, kind, latest, in, out, errOut)
	if err == nil {
		fmt.Fprintf(out, "\nGolder is up to date (at least %s). Run your command again to start the new version.\n", latest)
		return true, 0
	}
	fmt.Fprintf(errOut, "\ngolder: update could not be verified or completed: %v\n", err)
	choice, promptErr := services.choose(in, out, "Update failed", []string{"Continue with the current version", "Exit"}, choiceUpdate)
	if promptErr != nil {
		fmt.Fprintf(errOut, "golder: update prompt unavailable: %v\n", promptErr)
		return false, 0
	}
	if choice == choiceInterrupt {
		return true, 130
	}
	if choice == choiceSkipOnce {
		return true, 1
	}
	return false, 0
}

func installStartupUpdate(ctx context.Context, u *selfupdate.Updater, kind installKind, latest string, in io.Reader, out, errOut io.Writer) error {
	var err error
	if kind == installHomebrew {
		err = verifyHomebrewTarget(ctx, u.ExecPath)
		if err == nil {
			cmd := exec.CommandContext(ctx, "brew", "upgrade", "golder")
			cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, errOut
			err = cmd.Run()
		}
	} else {
		var installed string
		installed, err = executableVersion(ctx, u.ExecPath)
		if err == nil {
			var newer, comparable bool
			newer, comparable = selfupdate.UpdateAvailable(installed, latest)
			if !comparable {
				err = fmt.Errorf("cannot compare installed version %s", installed)
			} else if newer {
				u.BeforeReplace = func() error {
					version, err := executableVersion(ctx, u.ExecPath)
					if err != nil {
						return err
					}
					newer, comparable := selfupdate.UpdateAvailable(version, latest)
					if !comparable || !newer {
						return fmt.Errorf("installed version changed to %s during download; no replacement made", version)
					}
					return nil
				}
				err = u.Apply(ctx, latest, out)
			}
		}
	}
	if err == nil {
		err = verifyUpdatedExecutable(ctx, u.ExecPath, latest, kind)
	}
	return err
}

func verifyHomebrewTarget(ctx context.Context, original string) error {
	out, err := exec.CommandContext(ctx, "brew", "--prefix", "golder").Output()
	if err != nil {
		return fmt.Errorf("locate Homebrew's golder: %w", err)
	}
	path := filepath.Join(strings.TrimSpace(string(out)), "bin", "golder")
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != original {
		return fmt.Errorf("Homebrew's golder (%s) is not the running executable (%s); update the correct installation manually", path, original)
	}
	return nil
}

func verifyUpdatedExecutable(ctx context.Context, original, target string, kind installKind) error {
	path := original
	if kind == installHomebrew {
		// brew replaces a Cellar directory, not the resolved old executable.
		// Verify the invoked path, including a directly invoked Cellar binary.
		var err error
		path, err = exec.LookPath(os.Args[0])
		if err != nil {
			return fmt.Errorf("cannot find invoked executable %s: %w", os.Args[0], err)
		}
	}
	installed, err := executableVersion(ctx, path)
	if err != nil {
		return err
	}
	older, comparable := selfupdate.UpdateAvailable(installed, target)
	if !comparable || older {
		return fmt.Errorf("%s still reports %s; check for multiple installations (original: %s)", path, installed, original)
	}
	return nil
}

func executableVersion(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("check %s: %w", path, err)
	}
	fields := strings.Fields(string(output))
	if len(fields) < 2 || fields[0] != "golder" {
		return "", fmt.Errorf("cannot read version of %s", path)
	}
	return fields[1], nil
}
