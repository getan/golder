package agenttool

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNativeBrowserTimeoutReportsRouteWithoutGuessingCause(t *testing.T) {
	for _, tc := range []struct {
		mode, want string
	}{
		{browserProxyModeDirect, "browser proxy is off"},
		{browserProxyModeProxy, "browser proxy is enabled"},
	} {
		err := nativeBrowserStartupTimeout(tc.mode, context.DeadlineExceeded)
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "does not identify the cause") {
			t.Errorf("timeout %s = %v", tc.mode, err)
		}
	}
}
