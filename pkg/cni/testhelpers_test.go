package cni

import "context"

// CommandExecutorFunc is a function adapter for CommandExecutor. It is a
// test-only helper used to inject fake command execution.
type CommandExecutorFunc func(ctx context.Context, name string, stdin []byte, env []string) ([]byte, []byte, error)

// Execute implements CommandExecutor.
func (f CommandExecutorFunc) Execute(ctx context.Context, name string, stdin []byte, env []string) ([]byte, []byte, error) {
	return f(ctx, name, stdin, env)
}

// NewPluginManagerWithExecutor creates a plugin manager with a custom
// executor. It is a test-only seam; production code uses NewPluginManager.
func NewPluginManagerWithExecutor(pluginDir, configDir string, executor CommandExecutor) *PluginManager {
	if executor == nil {
		executor = NewDefaultCommandExecutor()
	}
	return &PluginManager{
		pluginDir: pluginDir,
		configDir: configDir,
		env:       []string{},
		executor:  executor,
	}
}
