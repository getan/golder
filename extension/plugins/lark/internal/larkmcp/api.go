package larkmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// rawAPI is the controlled `lark-cli api <METHOD> <PATH>` escape hatch. It is
// reachable only through cli_raw: cli_call stays limited to +shortcuts. The raw
// command is useful precisely where the typed CLI has no wrapper yet, but it
// must not become a way around the adapter's local-file and authentication
// boundaries.
func (p *Plugin) rawAPI(ctx context.Context, argv []string, localPath string, highRisk bool) Result {
	if len(argv) < 3 {
		return failure("lark", "ARGUMENT_BLOCKED", "api needs a method and an /open-apis path", "Use api GET /open-apis/... and cli_help api for the installed CLI")
	}
	method, apiPath, err := validateAPICall(argv[1], argv[2])
	if err != nil {
		return failure("lark", "ARGUMENT_BLOCKED", err.Error(), "Use api <GET|HEAD|POST|PUT|PATCH> /open-apis/...; DELETE and authentication endpoints are not exposed")
	}
	if method == "DELETE" {
		return failure("lark", "COMMAND_BLOCKED", "api DELETE is not exposed through MCP", "Use a typed command for destructive operations; api rejects DELETE by default")
	}
	write := method != "GET" && method != "HEAD"
	if write && !highRisk {
		return failure("lark", "HIGH_RISK_REQUIRED", "api "+method+" is a write and requires high_risk=true", "Set high_risk=true for POST/PUT/PATCH; the host still authorizes each exact call")
	}
	args, err := rewriteAPIArgs(argv, method, apiPath, localPath)
	if err != nil {
		return failure("lark", "ARGUMENT_BLOCKED", err.Error(), "Use inline --data/--params JSON and a workspace-relative --output path; local upload files require {file} plus path")
	}
	// The installed lark-cli api command does not accept --yes (it is not a
	// typed high-risk-write shortcut). high_risk remains an adapter-side gate:
	// writes are refused without it, and every call still needs exact-call host
	// authorization in tail.
	result := p.run(ctx, "lark", args, write)
	if result.OK && result.Verification != "preview" {
		result.OutputPath = apiOutputPath(args)
	}
	return result
}

// validateAPICall canonicalizes the method and confines the HTTP path to the
// public open-apis surface. The CLI accepts any path; the MCP adapter does not.
func validateAPICall(method, rawPath string) (string, string, error) {
	method = strings.ToUpper(strings.TrimSpace(method))
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE":
	default:
		return "", "", fmt.Errorf("api method %q is not supported", method)
	}
	apiPath, err := validateAPIPath(rawPath)
	if err != nil {
		return "", "", err
	}
	return method, apiPath, nil
}

func validateAPIPath(raw string) (string, error) {
	if !strings.HasPrefix(raw, "/open-apis/") {
		return "", errors.New("api path must start with /open-apis/")
	}
	if strings.ContainsAny(raw, "?#\\{}") {
		return "", errors.New("api path must be a plain path without query, fragment, backslash or braces")
	}
	for _, r := range raw {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '%' {
			return "", errors.New("api path must use plain path characters, not whitespace or percent-encoding")
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.RawQuery != "" || u.Fragment != "" || u.Path != raw {
		return "", errors.New("api path must be a relative /open-apis path")
	}
	if path.Clean(raw) != raw {
		return "", errors.New("api path must not contain empty or traversal segments")
	}
	lower := strings.ToLower(raw)
	for _, part := range strings.Split(lower, "/") {
		switch part {
		case "auth", "authen", "passport", "oauth", "tenant_access_token", "app_access_token", "access_token", "refresh_token":
			return "", errors.New("api authentication and token endpoints are not exposed")
		}
	}
	return raw, nil
}

// rewriteAPIArgs validates every flag the raw API may carry and rewrites the
// two local-file flags into absolute workspace paths. It deliberately rejects
// unknown flags: the API flag surface is small and stable enough for an
// allowlist, and a pass-through would reopen the boundary this function exists
// to enforce.
func rewriteAPIArgs(argv []string, method, apiPath, localPath string) ([]string, error) {
	out := []string{"api", method, apiPath}
	var fileValue, outputValue string
	var fileSeen, outputSeen, dataSeen, paramsSeen bool
	for i := 3; i < len(argv); i++ {
		key, value, hasValue, consumed, err := splitAPIFlag(argv, i)
		if err != nil {
			return nil, err
		}
		i += consumed - 1
		switch key {
		case "data", "params":
			if !hasValue {
				return nil, fmt.Errorf("--%s requires inline JSON", key)
			}
			if key == "data" {
				if dataSeen {
					return nil, errors.New("--data may be provided only once")
				}
				dataSeen = true
			} else {
				if paramsSeen {
					return nil, errors.New("--params may be provided only once")
				}
				paramsSeen = true
			}
			if value == "-" || strings.HasPrefix(strings.TrimSpace(value), "@") {
				return nil, fmt.Errorf("--%s must be inline JSON; @file and stdin are not allowed", key)
			}
			if !json.Valid([]byte(value)) {
				return nil, fmt.Errorf("--%s must be valid inline JSON", key)
			}
			out = append(out, "--"+key+"="+value)
		case "file":
			if !hasValue || value == "" {
				return nil, errors.New("--file requires {file} and the path option")
			}
			if fileSeen {
				return nil, errors.New("--file may be provided only once")
			}
			fileSeen, fileValue = true, value
		case "output":
			if !hasValue || value == "" {
				return nil, errors.New("--output requires a workspace-relative path or {file}")
			}
			if outputSeen {
				return nil, errors.New("--output may be provided only once")
			}
			outputSeen, outputValue = true, value
		case "jq":
			if !hasValue || hasControl(value) {
				return nil, errors.New("--jq requires a single-line expression")
			}
			out = append(out, "--jq="+value)
		case "page-delay", "page-limit", "page-size":
			if !hasValue {
				return nil, fmt.Errorf("--%s requires an integer value", key)
			}
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("--%s requires a non-negative integer", key)
			}
			out = append(out, "--"+key+"="+strconv.Itoa(n))
		case "page-all", "dry-run":
			if hasValue && value != "true" && value != "false" {
				return nil, fmt.Errorf("--%s accepts only true or false", key)
			}
			if hasValue {
				out = append(out, "--"+key+"="+value)
			} else {
				out = append(out, "--"+key)
			}
		case "as", "format", "json", "yes", "force", "no-confirm":
			return nil, fmt.Errorf("--%s is controlled by the adapter; omit it", key)
		case "help":
			return nil, errors.New("use cli_help api instead of --help in an execution request")
		default:
			return nil, fmt.Errorf("--%s is not an approved api flag", key)
		}
	}

	if strings.Count(fileValue, "{file}") > 1 || strings.Count(outputValue, "{file}") > 1 {
		return nil, errors.New("{file} may appear only once in a flag value")
	}
	filePlaceholder := fileSeen && strings.Contains(fileValue, "{file}")
	outputPlaceholder := outputSeen && strings.Contains(outputValue, "{file}")
	if filePlaceholder && outputPlaceholder {
		return nil, errors.New("{file} may be used only once across --file and --output")
	}
	if localPath != "" && !filePlaceholder && !outputPlaceholder {
		return nil, errors.New("path is only used when --file or --output contains {file}")
	}
	if filePlaceholder && strings.TrimSpace(localPath) == "" {
		return nil, errors.New("path is required when --file uses {file}")
	}
	if outputPlaceholder && outputValue != "{file}" {
		return nil, errors.New("--output must be exactly {file} or a concrete workspace-relative path")
	}
	if fileSeen && (!filePlaceholder || (fileValue != "{file}" && !strings.HasSuffix(fileValue, "={file}"))) {
		return nil, errors.New("--file must be {file} or field={file}")
	}
	var root string
	if outputSeen || fileSeen {
		var err error
		root, err = transferRoot()
		if err != nil {
			return nil, err
		}
	}
	if outputSeen {
		switch {
		case outputPlaceholder:
			target := localPath
			if strings.TrimSpace(target) == "" {
				target = defaultAPIOutputPath()
			}
			abs, err := resolveTransferPathIn(root, target, false)
			if err != nil {
				return nil, err
			}
			outputValue = abs
		case outputValue == "-":
			return nil, errors.New("--output - is not allowed; use a workspace-relative file path")
		default:
			abs, err := resolveTransferPathIn(root, outputValue, false)
			if err != nil {
				return nil, err
			}
			outputValue = abs
		}
		out = append(out, "--output="+outputValue)
	}
	if fileSeen {
		switch {
		case fileValue == "{file}":
			abs, err := resolveTransferPathIn(root, localPath, true)
			if err != nil {
				return nil, err
			}
			fileValue = abs
		case strings.HasSuffix(fileValue, "={file}"):
			abs, err := resolveTransferPathIn(root, localPath, true)
			if err != nil {
				return nil, err
			}
			fileValue = strings.TrimSuffix(fileValue, "{file}") + abs
		}
		out = append(out, "--file="+fileValue)
	}
	return out, nil
}

// defaultAPIOutputPath is the safe fallback for `api ... -o {file}` without an
// explicit path. It stays under the workspace's .golder/artifacts directory and
// carries a timestamp so concurrent downloads do not collide.
func defaultAPIOutputPath() string {
	name := "lark-api-" + time.Now().UTC().Format("20060102T150405.000000000Z") + ".bin"
	return filepath.Join(".golder", "artifacts", name)
}

// apiOutputPath extracts the absolute output path the adapter wrote into argv.
func apiOutputPath(args []string) string {
	const prefix = "--output="
	for _, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return strings.TrimPrefix(arg, prefix)
		}
	}
	return ""
}

// splitAPIFlag parses one long or short API flag. consumed is the number of
// argv entries the flag occupies, so the caller can advance over `--flag value`
// and `-o value` forms.
func splitAPIFlag(argv []string, i int) (key, value string, hasValue bool, consumed int, err error) {
	a := argv[i]
	if strings.HasPrefix(a, "--") {
		body := strings.TrimPrefix(a, "--")
		if body == "" {
			return "", "", false, 0, errors.New("-- terminator is not accepted")
		}
		if k, v, ok := strings.Cut(body, "="); ok {
			return k, v, true, 1, nil
		}
		if apiValueFlag(body) {
			if i+1 >= len(argv) || strings.HasPrefix(argv[i+1], "--") {
				return body, "", false, 1, nil
			}
			return body, argv[i+1], true, 2, nil
		}
		return body, "", false, 1, nil
	}
	if strings.HasPrefix(a, "-") && len(a) >= 2 {
		switch a[1] {
		case 'o':
			return shortAPIFlag(argv, i, "output", "o", a[2:])
		case 'q':
			return shortAPIFlag(argv, i, "jq", "q", a[2:])
		case 'h':
			return "help", "", false, 1, nil
		}
	}
	return "", "", false, 0, fmt.Errorf("unexpected api argument %q", a)
}

func shortAPIFlag(argv []string, i int, key, short, rest string) (string, string, bool, int, error) {
	if strings.HasPrefix(rest, "=") {
		return key, rest[1:], true, 1, nil
	}
	if rest != "" {
		return key, rest, true, 1, nil
	}
	if i+1 >= len(argv) || strings.HasPrefix(argv[i+1], "--") {
		return "", "", false, 0, fmt.Errorf("-%s requires a value", short)
	}
	return key, argv[i+1], true, 2, nil
}

func apiValueFlag(key string) bool {
	switch key {
	case "data", "params", "file", "output", "jq", "page-delay", "page-limit", "page-size":
		return true
	}
	return false
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
