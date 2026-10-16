//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckTokenFileMode(t *testing.T) {
	tests := []struct {
		mode        os.FileMode
		wantWarning bool
	}{
		{0o600, false},
		{0o644, true},
		{0o400, true},
	}

	for _, tt := range tests {
		t.Run(tt.mode.String(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".tahtoken")
			if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tt.mode); err != nil {
				t.Fatal(err)
			}

			warnings, err := checkTokenFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// The file is owned by the current user, so mode is the only possible warning.
			if got := len(warnings) > 0; got != tt.wantWarning {
				t.Errorf("warnings = %v, want a mode warning: %v", warnings, tt.wantWarning)
			}
		})
	}
}

func TestTestCommandPrintsModeWarning(t *testing.T) {
	f := newFakeHetzner(t)
	a, stdout, stderr := f.app()
	if err := os.Chmod(a.tokenPath, 0o644); err != nil {
		t.Fatal(err)
	}

	if code := run([]string{"tah", "test", "example.com"}, a); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Dangerous file mode: 644, use 0600!") {
		t.Errorf("stdout = %q, want the mode warning", stdout.String())
	}
}

func TestInitSetsMode0600(t *testing.T) {
	f := newFakeHetzner(t)
	a, _, stderr := f.app()
	if err := os.Chmod(a.tokenPath, 0o644); err != nil {
		t.Fatal(err)
	}

	if code := run([]string{"tah", "init"}, a); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	info, err := os.Stat(a.tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", info.Mode().Perm())
	}
}
