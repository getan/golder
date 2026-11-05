//go:build windows

package computeruse

import (
	"context"
	"errors"
	"net"
	"os"
	"unsafe"

	"github.com/getan/golder/internal/tuiosctl"
	"golang.org/x/sys/windows"
)

// SIO_AF_UNIX_GETPEERPID is _WSAIOR(IOC_VENDOR, 256). Windows supplies the
// connecting process ID; neither a path nor a claimed pane token is trusted.
const sioAFUnixGetPeerPID = 0x58000100

func operatorPeerIdentity(conn net.Conn) (int, uint64, error) {
	peer, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, 0, errors.New("computer pane requires a local Unix socket")
	}
	raw, err := peer.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var pid, returned uint32
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		socketErr = windows.WSAIoctl(windows.Handle(fd), sioAFUnixGetPeerPID, nil, 0, (*byte)(unsafe.Pointer(&pid)), uint32(unsafe.Sizeof(pid)), &returned, nil, 0)
	}); err != nil {
		return 0, 0, err
	}
	if socketErr != nil || returned != uint32(unsafe.Sizeof(pid)) || pid == 0 {
		return 0, 0, errors.New("computer pane peer PID unavailable")
	}
	start, err := windowsPaneProcessStart(pid)
	if err != nil {
		return 0, 0, err
	}
	return int(pid), start, nil
}

func windowsPaneProcessStart(pid uint32) (uint64, error) {
	if pid == 0 || pid == uint32(os.Getpid()) {
		return 0, ErrTargetLost
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return 0, errors.New("computer pane peer process unavailable")
	}
	defer windows.CloseHandle(process)
	wait, err := windows.WaitForSingleObject(process, 0)
	if err != nil || wait != uint32(windows.WAIT_TIMEOUT) {
		return 0, errors.New("computer pane peer process exited")
	}
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return 0, errors.New("computer pane peer account unavailable")
	}
	defer token.Close()
	owner, err := token.GetTokenUser()
	if err != nil {
		return 0, errors.New("computer pane peer account unavailable")
	}
	self := windows.GetCurrentProcessToken()
	current, err := self.GetTokenUser()
	if err != nil || !owner.User.Sid.Equals(current.User.Sid) {
		return 0, errors.New("computer pane peer account mismatch")
	}
	level, err := windowsElevation(token)
	selfLevel, selfErr := windowsElevation(self)
	if err != nil || selfErr != nil || level != selfLevel {
		return 0, errors.New("computer pane peer elevation mismatch")
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(process, &created, &exited, &kernel, &user); err != nil {
		return 0, errors.New("computer pane peer start time unavailable")
	}
	buffer := make([]uint16, 32768)
	length := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &length); err != nil || length == 0 {
		return 0, errors.New("computer pane peer executable unavailable")
	}
	peerInfo, err := os.Stat(windows.UTF16ToString(buffer[:length]))
	if err != nil {
		return 0, errors.New("computer pane peer executable unavailable")
	}
	selfPath, err := os.Executable()
	if err != nil {
		return 0, errors.New("computer pane host executable unavailable")
	}
	selfInfo, err := os.Stat(selfPath)
	if err != nil || !os.SameFile(peerInfo, selfInfo) {
		return 0, errors.New("computer pane peer executable mismatch")
	}
	start := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	if start == 0 {
		return 0, errors.New("computer pane peer start time unavailable")
	}
	return start, nil
}

func verifyPlatformOperatorPanePeer(ctx context.Context, daemon panePeerAuthority, windowID string, pid int, start uint64) error {
	client, ok := daemon.(*tuiosctl.Client)
	if !ok {
		return errors.New("computer pane daemon identity unavailable")
	}
	daemonPID, err := client.KernelPeerPID()
	if err != nil {
		return err
	}
	if err := verifyListedOperatorPane(ctx, daemon, windowID, pid, start); err != nil {
		return err
	}
	again, err := client.KernelPeerPID()
	if err != nil || again != daemonPID {
		return errors.New("computer pane daemon changed during verification")
	}
	parent, err := windowsPaneParent(uint32(pid), start)
	if err != nil || parent != daemonPID {
		return errors.New("computer pane is not a direct child of the verified daemon")
	}
	current, err := windowsPaneProcessStart(uint32(pid))
	if err != nil || current != start {
		return errors.New("computer pane peer process changed during verification")
	}
	return nil
}

func windowsPaneParent(pid uint32, expectedStart uint64) (uint32, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(process)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(process, &created, &exited, &kernel, &user); err != nil {
		return 0, err
	}
	start := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	if start == 0 || start != expectedStart {
		return 0, ErrTargetLost
	}
	var info windows.PROCESS_BASIC_INFORMATION
	if err := windows.NtQueryInformationProcess(process, windows.ProcessBasicInformation, unsafe.Pointer(&info), uint32(unsafe.Sizeof(info)), nil); err != nil {
		return 0, err
	}
	if info.UniqueProcessId != uintptr(pid) || info.InheritedFromUniqueProcessId == 0 || info.InheritedFromUniqueProcessId > uintptr(^uint32(0)) {
		return 0, ErrTargetLost
	}
	wait, err := windows.WaitForSingleObject(process, 0)
	if err != nil || wait != uint32(windows.WAIT_TIMEOUT) {
		return 0, ErrTargetLost
	}
	return uint32(info.InheritedFromUniqueProcessId), nil
}
