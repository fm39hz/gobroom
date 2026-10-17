package main

import (
	"strings"
	"testing"
)

func TestOperationArtifactsFlagIsAvailable(t *testing.T) {
	root := newRootCommand("/tmp/gobroom.sock", func(string, string) error { return nil })
	command, _, err := root.Find([]string{"connections", "test-operation"})
	if err != nil {
		t.Fatal(err)
	}
	if command.Flags().Lookup("artifacts-file") == nil {
		t.Fatal("test-operation is missing --artifacts-file")
	}
}

func TestReadProbeFilesRequiresStrictJSONArray(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{name: "object", json: `{"role":"image","path":"image.png"}`},
		{name: "null", json: `null`},
		{name: "unknown field", json: `[{"role":"image","path":"image.png","extra":true}]`},
		{name: "trailing value", json: `[{"role":"image","path":"image.png"}] {}`},
		{name: "malformed", json: `[{`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := readProbeFiles(strings.NewReader(test.json), "-")
			if err == nil {
				t.Fatalf("readProbeFiles(%q) succeeded, want an error", test.json)
			}
		})
	}
	files, err := readProbeFiles(strings.NewReader(`[{"role":"image","path":"image.png","mediaType":"image/png"}]`), "-")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Role != "image" || files[0].Path != "image.png" || files[0].MediaType != "image/png" {
		t.Fatalf("unexpected manifest decode: %#v", files)
	}
}

func TestOperationProbeStdinInputsAreMutuallyExclusive(t *testing.T) {
	if err := validateOperationStdin("-", "-"); err == nil {
		t.Fatal("both payload and artifacts using stdin was accepted")
	}
	for _, paths := range [][2]string{{"-", ""}, {"", "-"}, {"payload.json", "artifacts.json"}} {
		if err := validateOperationStdin(paths[0], paths[1]); err != nil {
			t.Fatalf("valid input paths %q and %q rejected: %v", paths[0], paths[1], err)
		}
	}
}

func TestReadOperationPayloadRejectsOversizeInput(t *testing.T) {
	_, err := readOperationPayload(strings.NewReader(strings.Repeat("x", 128*1024+1)), "-")
	if err == nil || !strings.Contains(err.Error(), "exceeds 128 KiB") {
		t.Fatalf("oversize payload error = %v, want 128 KiB limit error", err)
	}
}
