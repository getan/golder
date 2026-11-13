package agentcore

import "context"

// ToolchainGrant is host-authorized, exact-call directory capability data.
// Tool arguments cannot manufacture the context carrying it.
type ToolchainGrant struct {
	Name, Binding                  string
	Executable, GoRoot             string
	ReadDirs, WriteDirs, ReadFiles []string
}

func cloneToolchainGrant(g ToolchainGrant) ToolchainGrant {
	g.ReadDirs = append([]string(nil), g.ReadDirs...)
	g.WriteDirs = append([]string(nil), g.WriteDirs...)
	g.ReadFiles = append([]string(nil), g.ReadFiles...)
	return g
}

type toolchainGrantKey struct{}

func WithToolchainGrant(ctx context.Context, g ToolchainGrant) context.Context {
	return context.WithValue(ctx, toolchainGrantKey{}, cloneToolchainGrant(g))
}
func ToolchainGrantFromContext(ctx context.Context) (ToolchainGrant, bool) {
	g, ok := ctx.Value(toolchainGrantKey{}).(ToolchainGrant)
	return cloneToolchainGrant(g), ok
}
