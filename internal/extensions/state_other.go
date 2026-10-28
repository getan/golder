//go:build !unix

package extensions

import (
	"errors"
	"os"
)

func ownedByCurrentUser(os.FileInfo) bool { return false }
func lockState(string) (func(), error) {
	return nil, errors.New("persistent extensions require macOS or Linux")
}
