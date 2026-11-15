package adb

import (
	"strings"
	"testing"
)

func TestHelperLaunchCommandQuotesAndKeepsNonceOutOfArgv(t *testing.T) {
	command := HelperLaunchCommand("/data/app/golder/base.apk", "com.getan.golder.mobile", "com.getan.golder.mobile.androiduse.HelperMain", "abc'def", 10282)
	if !strings.Contains(command, "CLASSPATH='/data/app/golder/base.apk'") {
		t.Fatalf("missing quoted CLASSPATH: %s", command)
	}
	if !strings.Contains(command, "--nice-name='com.getan.golder.mobile:helper'") {
		t.Fatalf("missing nice name: %s", command)
	}
	if !strings.Contains(command, "'abc'\\''def'") {
		t.Fatalf("cert pin not shell-quoted: %s", command)
	}
	if strings.Contains(command, "nonce") {
		t.Fatal("nonce must not be in helper argv")
	}
}

func TestHelperStdin(t *testing.T) {
	got, err := HelperStdin("abc123")
	if err != nil || string(got) != "abc123\n" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := HelperStdin("bad\nnonce"); err == nil {
		t.Fatal("newline nonce must fail")
	}
}
