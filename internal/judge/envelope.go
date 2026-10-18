package judge

// The envelope: whether an approved call runs inside the OS sandbox, or outside
// it.
//
// Containment is what makes an approval mean something precise: the verdict
// says "the model may try this", the profile says "and here is the only place
// it can touch". For a command whose whole effect is readable from its text that
// floor costs nothing — and for a command that needs something the profile
// cannot give it costs everything, because the profile is a whitelist and a
// credential is not on it (see docs/sandboxing.md on why ~/.ssh is never
// grantable). `git push` over SSH is the shape that hurts: the command is
// harmless, the path it needs is a private key, and no path grant exists that
// could make the call work.
//
// So the rule is one sentence: a call may run unconfined when everything it
// touches is enumerable from its text, and nothing it touches is credential
// material. The first half is shellfacts' answer (internal/shellfacts/
// enumerable.go), which reports "I cannot read this" as a fact rather than
// guessing; the second half is the same list the credential gate uses, applied
// here so the sandbox stays the second line of defence even if the first one
// ever changes its mind.
//
// Two limits are deliberate and worth naming:
//
//   - a program that runs text we cannot read (a shell, an interpreter, `make`,
//     a wrapper whose real program is an argument) is contained. Its argv does
//     not bound its effect, and that is the whole question here. The list below
//     is short, each entry obvious, and its failure mode is containment;
//   - a program that runs the PROJECT's code (`go test`, `cargo build`) is NOT
//     on that list. Its own text bounds what was asked for, and containing every
//     build is what makes the sandbox unusable; a repository whose hooks or
//     build scripts you do not trust is a repository to run contained
//     (GOLDER_SANDBOX=enforce).

import (
	"path/filepath"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/shellfacts"
)

// runtimePrograms run text this layer cannot read: an inline program handed to
// an interpreter (`python3 -c …`, `sh -c …`), a script named on the command line
// (`sh deploy.sh`), a recipe file (`make`), a wrapper whose real program is one
// of its arguments (`env`, `xargs`, `sudo`, `nohup`, `timeout`), or a container
// whose mounts are decided by flags. Every one of them makes "the argv shows the
// effect" false, which is the only claim the envelope rests on.
var runtimePrograms = map[string]string{
	// Shells: they run whatever they are given, inline or from a file.
	"sh": "a shell runs the script it is given", "bash": "a shell runs the script it is given",
	"zsh": "a shell runs the script it is given", "dash": "a shell runs the script it is given",
	"ksh": "a shell runs the script it is given", "csh": "a shell runs the script it is given",
	"tcsh": "a shell runs the script it is given", "fish": "a shell runs the script it is given",
	"eval": "it runs its argument", "exec": "it runs its argument",
	"source": "it runs the file it names", ".": "it runs the file it names",
	// Interpreters.
	"python": "an interpreter runs the script it is given", "python2": "an interpreter runs the script it is given",
	"python3": "an interpreter runs the script it is given", "perl": "an interpreter runs the script it is given",
	"ruby": "an interpreter runs the script it is given", "node": "an interpreter runs the script it is given",
	"deno": "an interpreter runs the script it is given", "bun": "an interpreter runs the script it is given",
	"php": "an interpreter runs the script it is given", "lua": "an interpreter runs the script it is given",
	"luajit": "an interpreter runs the script it is given", "tclsh": "an interpreter runs the script it is given",
	"osascript": "an interpreter runs the script it is given", "Rscript": "an interpreter runs the script it is given",
	"awk": "it runs the program it is given", "gawk": "it runs the program it is given",
	"mawk": "it runs the program it is given", "sed": "its edit scripts and replacements can run commands",
	// Wrappers: the real program is one of their arguments, so `sudo ls` is not
	// `ls` and must not be read as one.
	"sudo": "it runs the command its arguments name", "doas": "it runs the command its arguments name",
	"su": "it runs the command its arguments name", "env": "it runs the command its arguments name",
	"xargs": "it runs the command with arguments it builds", "nohup": "it runs the command its arguments name",
	"setsid": "it runs the command its arguments name", "timeout": "it runs the command its arguments name",
	"stdbuf": "it runs the command its arguments name", "nice": "it runs the command its arguments name",
	"ionice": "it runs the command its arguments name", "time": "it runs the command its arguments name",
	"watch": "it re-runs the command its arguments name", "script": "it runs a shell",
	"expect": "it runs the program it is given", "caffeinate": "it runs the command its arguments name",
	// Recipe files and build runners whose work is read from a file, not argv.
	"make": "it runs the recipes in a makefile", "gmake": "it runs the recipes in a makefile",
	"bmake": "it runs the recipes in a makefile",
	// Containers: the mounts are flags, so the paths a command reaches are not
	// the paths on this line.
	"docker": "a container's mounts are decided by flags", "podman": "a container's mounts are decided by flags",
	"kubectl": "the work happens in a cluster", "container": "a container's mounts are decided by flags",
}

// EnvelopeBare reports whether command may run outside the OS sandbox, with a
// reason the note can carry. It is called only for a call the gate is ready to
// approve: the envelope narrows an approval, it never grants one.
func EnvelopeBare(command, lang string) (bool, string) {
	if strings.TrimSpace(command) == "" {
		return false, localized(lang, "命令是空的", "the command is empty")
	}
	plan := shellfacts.Parse(command)
	if ok, why := plan.Enumerable(); !ok {
		return false, localized(lang,
			"命令的效果无法从文本完整读出（"+why+"），继续保持隔离",
			"the command's effect cannot be read from its text ("+why+"), so it stays contained")
	}
	for _, c := range plan.AllCommands() {
		prog, literal := c.Program()
		if !literal {
			return false, localized(lang,
				"程序名是运行期算出来的，继续保持隔离",
				"the program itself is computed at runtime, so it stays contained")
		}
		name := filepath.Base(strings.TrimSpace(prog))
		if why, opaque := runtimePrograms[name]; opaque {
			return false, localized(lang,
				name+" 运行文本本身没有给出的代码（"+why+"），继续保持隔离",
				name+" runs code the line does not show ("+why+"), so it stays contained")
		}
	}
	for _, access := range plan.PathAccesses() {
		if access.Path == "" {
			continue
		}
		if _, secret := SecretPathReason(access.Path); secret {
			return false, localized(lang,
				"命令行里出现凭据路径 "+access.Path+"，继续保持隔离",
				"the line names credential material ("+access.Path+"), so it stays contained")
		}
	}
	if len(plan.Constructs) > 0 {
		return false, localized(lang, "命令行里有无法完整描述的结构，继续保持隔离",
			"the line has a construct this layer cannot describe, so it stays contained")
	}
	return true, localized(lang,
		"命令可完整解析：它触及的每个路径都在文本里，因此未隔离运行",
		"the command is fully enumerable — every path it touches is in its text — so it runs unconfined")
}

// envelopeFor answers the envelope question for one call: only a shell call has
// a command line to read, and a call whose arguments do not carry one (a
// malformed argument object) stays contained.
func envelopeFor(call agentcore.AgentToolCall, lang string) (bool, string) {
	if !strings.EqualFold(call.Name, "bash") {
		return false, ""
	}
	command, ok := bashCommand(call.Arguments)
	if !ok {
		return false, localized(lang, "命令行读不出来，继续保持隔离",
			"the command line could not be read, so it stays contained")
	}
	return EnvelopeBare(command, lang)
}
