//go:build windows

package browserctl

import "testing"

func TestCheckSocketWindowsPipe(t *testing.T) {
	if err := checkSocket(`\\.\pipe\golder-test`); err != nil {
		t.Fatal(err)
	}
	if err := checkSocket(`C:\tmp\control.sock`); err == nil {
		t.Fatal("filesystem path accepted as pipe")
	}
}
