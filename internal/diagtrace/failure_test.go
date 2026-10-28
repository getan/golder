package diagtrace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestErrorClassesNeverFormatPrivateErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, "none"}, {io.EOF, "eof"}, {os.ErrClosed, "closed_file"},
		{context.Canceled, "cancelled"}, {context.DeadlineExceeded, "deadline"},
		{syscall.EPIPE, "broken_pipe"}, {syscall.EIO, "io_error"},
		{syscall.EBADF, "bad_fd"}, {syscall.ENOTTY, "not_tty"},
		{errors.New("PRIVATE_ERROR_BODY"), "other"},
	} {
		err := tc.err
		if err != nil {
			err = fmt.Errorf("PRIVATE_ERROR_BODY: %w", err)
		}
		if got := ErrorClass(err); got != tc.want {
			t.Errorf("class=%s want=%s", got, tc.want)
		}
	}
}

func TestRecoverRecordsMetadataAndRethrows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.log")
	trace, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer trace.Close()
	const secret = "PRIVATE_PANIC_VALUE"
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		defer trace.Recover("test")
		panic(secret)
	}()
	if recovered != secret {
		t.Fatal("diagnostics swallowed or replaced the original panic")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"panic phase=test kind=string", "panic frame=", "TestRecoverRecordsMetadataAndRethrows"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(text, secret) || strings.Contains(text, "failure_test.go") || strings.Contains(text, filepath.Dir(path)) {
		t.Fatal("panic trace leaked values or source paths")
	}
}
