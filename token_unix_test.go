//go:build unix

package main

import (
	"fmt"
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
	if want := "Token file mode is 0644, should be 0600. Run: chmod 600 " + a.tokenPath; !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
}

// A token file of another user and group names both ids and the command
// that fixes the owner. Changing the owner of a real file needs root, so the
// comparison is tested on its own.
func TestOwnerWarningsFor(t *testing.T) {
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	if got := ownerWarningsFor("/x/.tahtoken", uid, gid, uid, gid); len(got) != 0 {
		t.Errorf("warnings = %v, want none for a file of the running user", got)
	}

	got := ownerWarningsFor("/x/.tahtoken", uid+1, gid+1, uid, gid)
	if len(got) != 2 {
		t.Fatalf("warnings = %v, want one for the user and one for the group", got)
	}
	wantUser := fmt.Sprintf("Token file /x/.tahtoken belongs to uid %d, but tah runs as uid %d. Run: sudo chown ", uid+1, uid)
	wantGroup := fmt.Sprintf("Token file /x/.tahtoken belongs to gid %d, but tah runs as gid %d. Run: sudo chgrp ", gid+1, gid)
	if !strings.HasPrefix(got[0], wantUser) || !strings.HasSuffix(got[0], " /x/.tahtoken") {
		t.Errorf("user warning = %q, want prefix %q", got[0], wantUser)
	}
	if !strings.HasPrefix(got[1], wantGroup) || !strings.HasSuffix(got[1], " /x/.tahtoken") {
		t.Errorf("group warning = %q, want prefix %q", got[1], wantGroup)
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
