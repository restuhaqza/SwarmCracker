package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestWrapDeprecatedPreservesPreRun guards against the regression where the
// deprecation wrapper replaced a command's PreRun hook wholesale. For `join`
// that hook captures the manager address; dropping it made the deprecated
// top-level `swarmcracker join <addr>` always probe an empty address.
func TestWrapDeprecatedPreservesPreRun(t *testing.T) {
	called := false
	original := func(cmd *cobra.Command, args []string) { called = true }

	cmd := &cobra.Command{Use: "legacy", PreRun: original}
	wrapped := wrapDeprecated(cmd, "new legacy")

	if wrapped.PreRun == nil {
		t.Fatal("expected PreRun to be set")
	}
	if wrapped.Deprecated == "" {
		t.Fatal("expected Deprecated message to be set")
	}

	wrapped.PreRun(wrapped, []string{"some-arg"})
	if !called {
		t.Fatal("original PreRun hook was not called")
	}
}
