package main

import (
	"bytes"
	"testing"
)

func TestRootWithoutSubcommandRunsBundledTUI(t *testing.T) {
	called := 0
	root := newRootCommand("/tmp/default.sock", func(path, gotVersion string) error {
		called++
		if path != "/tmp/override.sock" || gotVersion != version {
			t.Fatalf("TUI args path=%q version=%q", path, gotVersion)
		}
		return nil
	})
	root.SetArgs([]string{"--ipc", "/tmp/override.sock"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("bundled TUI called %d times, want 1", called)
	}
}

func TestRootHelpAndTypedCommandsDoNotRequireSeparateTUIBinary(t *testing.T) {
	called := 0
	root := newRootCommand("/tmp/default.sock", func(string, string) error { called++; return nil })
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if called != 0 {
		t.Fatal("--help unexpectedly launched TUI")
	}
	for _, path := range [][]string{{"status"}, {"providers", "list"}, {"providers", "catalog"}, {"connections", "test"}, {"connections", "preview-models"}, {"connections", "refresh-models"}, {"physical-models", "list"}, {"combo-models", "list"}, {"combo-models", "strategies"}} {
		command, _, err := root.Find(path)
		if err != nil || command == root {
			t.Fatalf("typed command %v missing: command=%v err=%v", path, command, err)
		}
	}
	for _, item := range []struct {
		path []string
		flag string
	}{{[]string{"combo-models", "upsert"}, "strategy-options"}, {[]string{"physical-models", "upsert"}, "policy-options"}, {[]string{"connections", "refresh-models"}, "model-id"}, {[]string{"connections", "refresh-models"}, "entitlements-only"}} {
		command, _, err := root.Find(item.path)
		if err != nil || command.Flags().Lookup(item.flag) == nil {
			t.Fatalf("typed command %v missing flag --%s: command=%v err=%v", item.path, item.flag, command, err)
		}
	}
	if root.PersistentFlags().Lookup("ipc") == nil || root.PersistentFlags().Lookup("json") == nil {
		t.Fatal("root persistent CLI flags were not preserved")
	}
}
