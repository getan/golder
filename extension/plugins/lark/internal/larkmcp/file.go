package larkmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxTransferFileSize = 128 << 20

func (p *Plugin) file(ctx context.Context, d definition, in input) Result {
	service, _ := in.Options["service"].(string)
	path, _ := in.Options["path"].(string)
	argv, err := stringList(in.Options["argv"])
	if err != nil {
		return fileFailure(err)
	}
	pathLen := commandPathLen(argv, 3)
	if err := validateCommandPath(service, argv[:pathLen], false); err != nil {
		return failure("lark", "COMMAND_BLOCKED", err.Error(), "Use a Lark/Project command path")
	}
	if err := validateFileArgs(argv[pathLen:]); err != nil {
		return failure("lark", "ARGUMENT_BLOCKED", err.Error(), "The file adapter controls the path; do not pass @file or stdin")
	}
	placeholders := 0
	for _, arg := range argv {
		placeholders += strings.Count(arg, "{file}")
	}
	if placeholders != 1 {
		return fileFailure(errors.New("argv must contain exactly one {file} placeholder"))
	}
	upload := in.Operation == "upload"
	root, err := transferRoot()
	if err != nil {
		return fileFailure(err)
	}
	absolute, err := resolveTransferPathIn(root, path, upload)
	if err != nil {
		return fileFailure(err)
	}
	args := make([]string, len(argv))
	for i, arg := range argv {
		args[i] = strings.ReplaceAll(arg, "{file}", absolute)
	}
	if high, _ := in.Options["high_risk"].(bool); high {
		args = append(args, "--yes")
	}
	_ = d
	return p.runIn(ctx, service, root, args, true)
}

func fileFailure(err error) Result {
	return failure("lark", "INVALID_ARGUMENTS", err.Error(), "Use a workspace-relative path and a CLI argv with one {file} placeholder")
}

func stringList(value any) ([]string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, errors.New("argv must be an array of strings")
	}
	if len(out) < 2 {
		return nil, errors.New("argv must include a command family and operation")
	}
	return out, nil
}

func validateFileArgs(args []string) error {
	flags, err := parseBusinessFlags(args)
	if err != nil {
		return err
	}
	for _, flag := range flags {
		key, value := flag.key, flag.value
		switch key {
		case "as", "format", "yes", "force", "no-confirm":
			return fmt.Errorf("--%s is controlled by the adapter", key)
		}
		for _, blocked := range []string{"secret", "credential", "header", "config", "profile", "browser", "proxy", "debug", "verbose", "endpoint", "host"} {
			if strings.Contains(key, blocked) {
				return fmt.Errorf("--%s is a configuration or credential flag and is not exposed", key)
			}
		}
		if localPathFlag(key) && !strings.Contains(value, "{file}") {
			return fmt.Errorf("--%s must use the {file} placeholder", key)
		}
		if remoteResourceFlag(key) {
			if value == "" || !validRemoteIdentifier(value, strings.HasSuffix(key, "s")) {
				return fmt.Errorf("--%s must be an inline remote identifier, not a local file, URL or stdin reference", key)
			}
			continue
		}
		if !strings.Contains(value, "{file}") {
			if strings.HasPrefix(strings.TrimSpace(value), "@") || value == "-" || strings.ContainsAny(value, "\x00\r\n") {
				return errors.New("local-file/stdin references are not allowed")
			}
			if strings.Contains(key, "url") && strings.Contains(value, "://") {
				if err := resourceURL(value); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func flagSegments(key string) []string {
	return strings.FieldsFunc(strings.ToLower(key), func(r rune) bool { return r == '-' || r == '_' })
}

// remoteResourceFlag reports whether a flag carries an object identifier from
// the Lark server (--file-key, --image-key, --file-token) rather than a local
// path. The last name segment decides, so --file-key is a remote key while
// --key-file remains a local file parameter.
func remoteResourceFlag(key string) bool {
	segments := flagSegments(key)
	if len(segments) == 0 {
		return false
	}
	switch segments[len(segments)-1] {
	case "key", "keys", "token", "tokens", "id", "ids", "uuid", "uuids", "code", "codes":
		return true
	}
	return false
}

// validRemoteIdentifier accepts an inline server-side identifier such as
// file_v3_… or om_…, and rejects anything that looks like a local path, a URL or
// a stdin/@file reference. Remote identifiers never contain path separators.
func validRemoteIdentifier(value string, list bool) bool {
	if !validResourceValue(value, list) {
		return false
	}
	parts := []string{value}
	if list {
		parts = strings.Split(value, ",")
	}
	for _, part := range parts {
		token := strings.TrimSpace(part)
		if token == "" || token == "." || token == ".." || strings.ContainsAny(token, `/\`) || strings.HasPrefix(token, "~") {
			return false
		}
	}
	return true
}

// localPathFlag reports whether a flag names a local filesystem path. Whole name
// segments are classified instead of raw substrings, so a remote identifier such
// as --file-key is not mistaken for the local --file or --output.
func localPathFlag(key string) bool {
	if remoteResourceFlag(key) {
		return false
	}
	for _, segment := range flagSegments(key) {
		for _, token := range []string{"file", "path", "output", "dir", "image", "audio", "video", "attachment", "media"} {
			if strings.Contains(segment, token) {
				return true
			}
		}
	}
	return false
}

func resolveTransferPath(raw string, upload bool) (string, error) {
	root, err := transferRoot()
	if err != nil {
		return "", err
	}
	return resolveTransferPathIn(root, raw, upload)
}

// transferRoot is the exact cwd both the plugin path check and lark-cli must
// use. It is resolved once so a symlinked project path cannot produce two
// different allowlist roots.
func transferRoot() (string, error) {
	root, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(root)
}

func resolveTransferPathIn(root, raw string, upload bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || filepath.IsAbs(raw) {
		return "", errors.New("path must be a non-empty workspace-relative path")
	}
	absolute := filepath.Join(root, filepath.Clean(raw))
	if !insideWorkspace(root, absolute) {
		return "", errors.New("path escapes the workspace")
	}
	if upload {
		real, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return "", err
		}
		if !insideWorkspace(root, real) {
			return "", errors.New("file resolves outside the workspace")
		}
		info, err := os.Stat(real)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", errors.New("upload path must be a regular file")
		}
		if info.Size() > maxTransferFileSize {
			return "", errors.New("file exceeds the transfer size limit")
		}
		return real, nil
	}
	parent := filepath.Dir(absolute)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	if !insideWorkspace(root, realParent) {
		return "", errors.New("output path resolves outside the workspace")
	}
	return absolute, nil
}

func insideWorkspace(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
