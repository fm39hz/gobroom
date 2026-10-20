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
	for _, path := range [][]string{{"status"}, {"logs"}, {"route-explain"}, {"persistence", "status"}, {"persistence", "retry"}, {"providers", "list"}, {"providers", "catalog"}, {"extensions", "catalog"}, {"connections", "test"}, {"connections", "test-inference"}, {"connections", "test-operation"}, {"connections", "preview-models"}, {"connections", "refresh-models"}, {"connections", "setup", "start"}, {"connections", "setup", "get"}, {"connections", "setup", "submit"}, {"connections", "setup", "cancel"}, {"physical-models", "list"}, {"combo-models", "list"}, {"combo-models", "strategies"}, {"compatibility", "loss-ceiling", "get"}, {"compatibility", "loss-ceiling", "set"}} {
		command, _, err := root.Find(path)
		if err != nil || command == root {
			t.Fatalf("typed command %v missing: command=%v err=%v", path, command, err)
		}
	}
	for _, name := range []string{"authorize-start", "authorize-get", "authorize-complete", "authorize-cancel"} {
		if command, remaining, err := root.Find([]string{"connections", name}); err == nil && len(remaining) == 0 && command.Name() == name {
			t.Fatalf("removed authorization command %q is still registered as %q", name, command.CommandPath())
		}
	}
	for _, item := range []struct {
		path []string
		flag string
	}{{[]string{"route-explain"}, "request-file"}, {[]string{"route-explain"}, "path"}, {[]string{"combo-models", "upsert"}, "strategy-options"}, {[]string{"combo-models", "upsert"}, "allow-loss"}, {[]string{"combo-models", "upsert"}, "deny-loss"}, {[]string{"physical-models", "upsert"}, "policy-options"}, {[]string{"physical-models", "upsert"}, "allow-loss"}, {[]string{"physical-models", "upsert"}, "deny-loss"}, {[]string{"compatibility", "loss-ceiling", "set"}, "allow-only"}, {[]string{"compatibility", "loss-ceiling", "set"}, "allow"}, {[]string{"compatibility", "loss-ceiling", "set"}, "deny"}, {[]string{"connections", "refresh-models"}, "model-id"}, {[]string{"connections", "refresh-models"}, "entitlements-only"}, {[]string{"connections", "test-operation"}, "operation"}, {[]string{"connections", "test-operation"}, "model-id"}, {[]string{"connections", "test-operation"}, "payload-file"}, {[]string{"connections", "setup", "start"}, "connection-id"}, {[]string{"connections", "setup", "get"}, "session-id"}, {[]string{"connections", "setup", "submit"}, "input-file"}, {[]string{"connections", "setup", "cancel"}, "session-id"}, {[]string{"logs"}, "limit"}} {
		command, _, err := root.Find(item.path)
		if err != nil || command.Flags().Lookup(item.flag) == nil {
			t.Fatalf("typed command %v missing flag --%s: command=%v err=%v", item.path, item.flag, command, err)
		}
	}
	if root.PersistentFlags().Lookup("ipc") == nil || root.PersistentFlags().Lookup("json") == nil {
		t.Fatal("root persistent CLI flags were not preserved")
	}
}
