// Package mermaidwasm runs the pinned upstream Mermaid WebAssembly module
// without a browser, JavaScript runtime, network access, or WASI filesystem.
package mermaidwasm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// WASM SHA-256 from mermaid-wasm-renderer 0.3.1's npm release. The pinned
// tarball and its reproducible acquisition are described in the GUI docs.
const ModuleSHA256 = "00d369c98933d0bac9408d9554cef6521b7e62a4ebfe36d9734502d3e73a0cbf"

const (
	maxModuleBytes = 4 << 20
	maxInputBytes  = 32 << 10
	maxOutputBytes = 2 << 20
	maxFontBytes   = 32 << 20
	// Allow for slow Intel hosts while retaining a hard bound on guest execution.
	timeLimit = 15 * time.Second
)

var slots = make(chan struct{}, 2)

// Render creates an isolated instance for one diagram. It accepts only the
// exact pinned module and exposes no host filesystem, clock or network.
func Render(ctx context.Context, module []byte, source string) (string, error) {
	return RenderWithFont(ctx, module, source, nil)
}

// RenderWithFont registers an explicitly supplied font for label measurement.
// A suitable system font keeps CJK labels from being laid out as Latin text.
func RenderWithFont(ctx context.Context, module []byte, source string, font []byte) (string, error) {
	if len(module) == 0 || len(module) > maxModuleBytes || len(source) == 0 || len(source) > maxInputBytes {
		return "", errors.New("invalid Mermaid module or input size")
	}
	if len(font) > maxFontBytes {
		return "", errors.New("Mermaid font exceeds size limit")
	}
	sum := sha256.Sum256(module)
	if hex.EncodeToString(sum[:]) != ModuleSHA256 {
		return "", errors.New("Mermaid module checksum mismatch")
	}
	if err := validateDiagram(source); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, timeLimit)
	defer cancel()
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	config := wazero.NewRuntimeConfigInterpreter().WithMemoryLimitPages(2048).WithCloseOnContextDone(true)
	runtime := wazero.NewRuntimeWithConfig(ctx, config)
	defer runtime.Close(context.Background())
	compiled, err := runtime.CompileModule(ctx, module)
	if err != nil {
		return "", fmt.Errorf("compile Mermaid module: %w", err)
	}
	var guestError string
	builders := map[string]wazero.HostModuleBuilder{}
	for _, imported := range compiled.ImportedFunctions() {
		mod, name, ok := imported.Import()
		if !ok || mod != "./mermaid_wasm_renderer_bg.js" {
			return "", fmt.Errorf("unexpected Mermaid module import %q.%q", mod, name)
		}
		builder := builders[mod]
		if builder == nil {
			builder = runtime.NewHostModuleBuilder(mod)
			builders[mod] = builder
		}
		var fn api.GoModuleFunc
		switch {
		case strings.HasPrefix(name, "__wbg_Error_") && len(imported.ParamTypes()) == 2 && len(imported.ResultTypes()) == 1:
			fn = func(_ context.Context, instance api.Module, stack []uint64) {
				n := min(uint32(stack[1]), uint32(4096))
				if data, ok := instance.Memory().Read(uint32(stack[0]), n); ok {
					guestError = string(data)
				}
				stack[0] = 0
			}
		case name == "__wbindgen_init_externref_table" && len(imported.ParamTypes()) == 0 && len(imported.ResultTypes()) == 0:
			fn = func(context.Context, api.Module, []uint64) {}
		default:
			return "", fmt.Errorf("unsupported Mermaid module import %q", name)
		}
		builder.NewFunctionBuilder().WithGoModuleFunction(fn, imported.ParamTypes(), imported.ResultTypes()).Export(name)
	}
	for _, builder := range builders {
		if _, err = builder.Instantiate(ctx); err != nil {
			return "", fmt.Errorf("instantiate Mermaid imports: %w", err)
		}
	}
	instance, err := runtime.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithStartFunctions())
	if err != nil {
		return "", fmt.Errorf("instantiate Mermaid module: %w", err)
	}
	call := func(name string, args ...uint64) ([]uint64, error) {
		f := instance.ExportedFunction(name)
		if f == nil {
			return nil, fmt.Errorf("missing Mermaid export %q", name)
		}
		return f.Call(ctx, args...)
	}
	if _, err = call("__wbindgen_start"); err != nil {
		return "", err
	}
	if len(font) > 0 {
		ptr, err := call("__wbindgen_malloc", uint64(len(font)), 1)
		if err != nil || len(ptr) != 1 || ptr[0] == 0 || !instance.Memory().Write(uint32(ptr[0]), font) {
			return "", errors.New("cannot copy Mermaid font")
		}
		if _, err := call("registerFont", ptr[0], uint64(len(font))); err != nil {
			return "", fmt.Errorf("register Mermaid font: %w", err)
		}
	}
	allocated, err := call("__wbindgen_malloc", uint64(len(source)), 1)
	if err != nil || len(allocated) != 1 || allocated[0] == 0 {
		return "", errors.New("cannot allocate Mermaid input")
	}
	if !instance.Memory().Write(uint32(allocated[0]), []byte(source)) {
		return "", errors.New("cannot write Mermaid input")
	}
	output, err := call("renderSvgWithConfig", allocated[0], uint64(len(source)), 0, 0, 0, 0)
	if err != nil {
		return "", fmt.Errorf("Mermaid render failed: %w", err)
	}
	if len(output) != 4 {
		return "", errors.New("invalid Mermaid return value")
	}
	if output[3] != 0 {
		_, _ = call("__externref_table_dealloc", output[2])
		if guestError == "" {
			guestError = "invalid Mermaid diagram"
		}
		return "", errors.New(guestError)
	}
	ptr, n := uint32(output[0]), uint32(output[1])
	if n == 0 || n > maxOutputBytes {
		return "", errors.New("Mermaid output exceeds limit")
	}
	data, ok := instance.Memory().Read(ptr, n)
	if !ok {
		return "", errors.New("invalid Mermaid output address")
	}
	svg := string(data)
	_, _ = call("__wbindgen_free", uint64(ptr), uint64(n), 1)
	return svg, nil
}

// The upstream renderer silently treats unknown diagram headers as flowcharts.
// Limit the initial implementation to tested kinds and reject front matter and
// initialization directives instead of silently ignoring their semantics.
func validateDiagram(source string) error {
	if strings.Contains(source, "%%{") || strings.ContainsRune(source, '\x00') {
		return errors.New("unsupported Mermaid directives")
	}
	for _, raw := range strings.Split(source, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "%%") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "flowchart", "graph", "sequenceDiagram", "classDiagram", "stateDiagram-v2", "erDiagram":
			return nil
		default:
			return errors.New("unsupported Mermaid diagram type")
		}
	}
	return errors.New("empty Mermaid diagram")
}
