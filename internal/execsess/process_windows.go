//go:build windows

package execsess

import (
	"os/exec"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/getan/golder/internal/proc"
)

// terminationGracePeriod mirrors the unix build; Windows Kill falls back to
// os.Process.Kill, which is immediate.
const terminationGracePeriod = 50 * time.Millisecond

// configurePipeProcess hides the console Windows would allocate for a console
// subsystem child of the GUI process. Without this, every bash call flashes a
// visible conhost window for the lifetime of the command.
func configurePipeProcess(cmd *exec.Cmd) {
	proc.Configure(cmd)
}

// configureTTYProcess is unreachable on Windows: openPTY already fails a
// tty=true request before the process is configured.
func configureTTYProcess(_ *exec.Cmd) {}

// newProcessTree creates a job object that kills every attached process when
// its last handle closes. The child is assigned immediately after Start so a
// cancelled or timed-out shell cannot leave git/rg descendants behind.
func newProcessTree(cmd *exec.Cmd) uintptr {
	if cmd.Process == nil {
		return 0
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return 0
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		_ = windows.CloseHandle(job)
		return 0
	}
	err = windows.AssignProcessToJobObject(job, process)
	_ = windows.CloseHandle(process)
	if err != nil {
		_ = windows.CloseHandle(job)
		return 0
	}
	return uintptr(job)
}

func closeProcessTree(tree uintptr) {
	if tree != 0 {
		_ = windows.CloseHandle(windows.Handle(tree))
	}
}

func terminateProcessGroup(_ int) bool { return false }

func killProcessGroup(_ int) {}

func interruptProcessGroup(_ int) bool { return false }
