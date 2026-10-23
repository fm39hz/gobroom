package main

import (
	"bytes"
	"os"
	"os/exec"
	"testing"
)

func TestDaemonUsageFlagHelpHelper(t *testing.T) {
	if os.Getenv("GOBROOM_HELP_TEST_CHILD") != "1" {
		return
	}
	os.Args = []string{"gobroomd", "-h"}
	main()
}

func TestDaemonHelpDoesNotExposeControlTokenEnvironment(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestDaemonUsageFlagHelpHelper$")
	command.Env = append(os.Environ(), "GOBROOM_HELP_TEST_CHILD=1", "GOBROOM_CONTROL_TOKEN=private-control-token-env-marker")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("daemon help failed: %v", err)
	}
	if bytes.Contains(output, []byte("private-control-token-env-marker")) {
		t.Fatal("daemon help exposed the control token environment value")
	}
	if !bytes.Contains(output, []byte("usage-capture-raw")) || !bytes.Contains(output, []byte("control-token")) {
		t.Fatal("daemon help did not expose the intended configuration flags")
	}
}
