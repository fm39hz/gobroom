package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartupBundleInputIsStrictAndBounded(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"valid", `{"version":9}`, true},
		{"duplicate", `{"version":9,"version":9}`, false},
		{"unknown", `{"version":9,"private-secret":true}`, false},
		{"trailing", `{"version":9}{"version":9}`, false},
		{"null", `null`, false},
		{"array", `[]`, false},
		{"oversize", `{"version":9}` + strings.Repeat(" ", 32<<20), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bundle.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			bundle, err := readStartupBundle(path)
			if tc.valid {
				if err != nil || bundle == nil || bundle.Version != 9 {
					t.Fatalf("valid bundle: %#v err=%v", bundle, err)
				}
			} else if err == nil {
				t.Fatal("invalid startup bundle accepted")
			} else if strings.Contains(err.Error(), "private-secret") {
				t.Fatal("raw configuration escaped parser error")
			}
		})
	}
	if bundle, err := readStartupBundle(""); err != nil || bundle != nil {
		t.Fatalf("unset optional file: %#v %v", bundle, err)
	}
	if _, err := readStartupBundle(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing file accepted")
	}
}
