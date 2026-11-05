package computeruse

import (
	"errors"
	"strconv"
	"strings"
)

// linuxProcessStart returns field 22 of /proc/PID/stat. The command name in
// parentheses can contain spaces and right parentheses, so split after its
// last closing parenthesis rather than splitting the whole line on spaces.
func linuxProcessStart(stat string, expectedPID int) (uint64, error) {
	bad := errors.New("computer pane peer process identity unavailable")
	open, close := strings.IndexByte(stat, '('), strings.LastIndexByte(stat, ')')
	if open < 1 || close <= open || close+1 >= len(stat) {
		return 0, bad
	}
	pid, err := strconv.Atoi(strings.TrimSpace(stat[:open]))
	if err != nil || pid != expectedPID || pid <= 0 {
		return 0, bad
	}
	fields := strings.Fields(stat[close+1:])
	const startIndex = 19 // field 22, with field 3 as index zero
	if len(fields) <= startIndex {
		return 0, bad
	}
	start, err := strconv.ParseUint(fields[startIndex], 10, 64)
	if err != nil || start == 0 {
		return 0, bad
	}
	return start, nil
}
