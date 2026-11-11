//go:build !unix

package extensions

import (
	"context"
	"errors"
	"os"
)

func ownedByCurrentUser(os.FileInfo) bool { return false }
func lockState(string) (func(), error) {
	return nil, errors.New("persistent extensions require macOS or Linux")
}

func lockStateContext(context.Context, string) (func(), error) {
	return nil, errors.New("persistent extensions require macOS or Linux")
}

func stateLockMode(context.Context, string, bool) (func(), error) {
	return nil, errors.New("persistent extensions require macOS or Linux")
}
