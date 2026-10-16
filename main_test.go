package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGetDomain(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"example.com", "example.com"},
		{"sub.example.com", "example.com"},
		{"_acme-challenge.sub.example.com", "example.com"},
		{"example.com.", "example.com"},
		{"deep.nested.sub.example.co.uk", "example.co.uk"},
		{"example.co.uk", "example.co.uk"},
		{"test.example.org", "example.org"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := getDomain(tt.input)
			if got != tt.expected {
				t.Errorf("getDomain(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestGetSub(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"example.com", ""},
		{"sub.example.com", "sub"},
		{"_acme-challenge.sub.example.com", "_acme-challenge.sub"},
		{"example.com.", ""},
		{"deep.nested.example.com", "deep.nested"},
		{"_acme-challenge.example.com", "_acme-challenge"},
		{"a.b.c.example.co.uk", "a.b.c"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := getSub(tt.input)
			if got != tt.expected {
				t.Errorf("getSub(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// runCmd runs tah with the given arguments against f and returns exit code, stdout, stderr.
func runCmd(f *fakeHetzner, args ...string) (int, string, string) {
	a, stdout, stderr := f.app()
	code := run(append([]string{"tah"}, args...), a)
	return code, stdout.String(), stderr.String()
}

func TestRunUsageErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"no command", nil, "Command is missing!"},
		{"invalid command", []string{"delete"}, "Invalid command: delete!"},
		{"-v is not a version alias", []string{"-v"}, "Invalid command: -v!"},
		{"set without domain", []string{"set"}, "domain is missing!"},
		{"set without validation_name", []string{"set", "example.com"}, "validation_name is missing!"},
		{"set without validation_context", []string{"set", "example.com", "_acme-challenge.example.com"}, "validation_context is missing!"},
		{"unset without validation_context", []string{"unset", "example.com", "_acme-challenge.example.com"}, "validation_context is missing!"},
		{"test without domain", []string{"test"}, "domain is missing!"},
		{"domain without public suffix", []string{"test", "localhost"}, "Invalid domain: localhost!"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeHetzner(t)
			code, stdout, stderr := runCmd(f, tt.args...)
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if !strings.Contains(stderr, tt.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tt.wantErr)
			}
			if !strings.Contains(stdout, `Use "tah help" for help!`) {
				t.Errorf("stdout = %q, want the help hint", stdout)
			}
			if len(f.calls) != 0 {
				t.Errorf("usage error must not reach the API, got calls %v", f.calls)
			}
		})
	}
}

func TestRunWithoutArgs(t *testing.T) {
	f := newFakeHetzner(t)
	a, stdout, _ := f.app()
	if code := run(nil, a); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stdout.String(), `Use "tah help"`) {
		t.Errorf("stdout = %q, want the help hint with the default program name", stdout.String())
	}
}

func TestHelp(t *testing.T) {
	code, stdout, stderr := runCmd(newFakeHetzner(t), "help")
	if code != 0 || stderr != "" {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	for _, want := range []string{"Usage: tah <command>", "\tset\n", "\tunset\n", "\tinit", "\ttest", "\tversion", "\thelp"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help output missing %q:\n%s", want, stdout)
		}
	}
}

func TestVersion(t *testing.T) {
	old := Version
	Version = "v9.9.9"
	t.Cleanup(func() { Version = old })

	for _, arg := range []string{"version", "--version"} {
		t.Run(arg, func(t *testing.T) {
			code, stdout, _ := runCmd(newFakeHetzner(t), arg)
			if code != 0 || stdout != "v9.9.9\n" {
				t.Errorf("exit code = %d, stdout = %q, want 0 and %q", code, stdout, "v9.9.9\n")
			}
		})
	}
}

func TestSetCreatesRRSet(t *testing.T) {
	f := newFakeHetzner(t)

	code, _, stderr := runCmd(f, "set", "nas.example.com", "_acme-challenge.nas.example.com.", "token-value")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}

	got, ok := f.records("_acme-challenge.nas")
	if !ok || !slices.Equal(got, []string{`"token-value"`}) {
		t.Errorf("records = %v (exists %v), want the quoted value", got, ok)
	}

	req := f.lastCreate
	if req.Type != "TXT" || req.TTL == nil || *req.TTL != 3600 {
		t.Errorf("create request type = %q ttl = %v, want TXT and 3600", req.Type, req.TTL)
	}
}

func TestSetAddsToExistingRRSet(t *testing.T) {
	f := newFakeHetzner(t)
	f.addRecords("_acme-challenge", `"first"`)

	code, _, stderr := runCmd(f, "set", "example.com", "_acme-challenge.example.com", "second")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}

	got, _ := f.records("_acme-challenge")
	if !slices.Equal(got, []string{`"first"`, `"second"`}) {
		t.Errorf("records = %v, want both values", got)
	}
	if f.called("POST /zones/42/rrsets") {
		t.Error("set must add to the existing RRSet, not create a new one")
	}
}

func TestUnsetRemovesRecord(t *testing.T) {
	f := newFakeHetzner(t)
	f.addRecords("_acme-challenge", `"keep"`, `"drop"`)

	code, _, stderr := runCmd(f, "unset", "example.com", "_acme-challenge.example.com", "drop")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}

	got, _ := f.records("_acme-challenge")
	if !slices.Equal(got, []string{`"keep"`}) {
		t.Errorf("records = %v, want only the other value left", got)
	}
}

func TestUnsetRRSetNotFound(t *testing.T) {
	code, _, stderr := runCmd(newFakeHetzner(t), "unset", "example.com", "_acme-challenge.example.com", "x")
	if code != 1 || !strings.Contains(stderr, "RRSet not found") {
		t.Errorf("exit code = %d, stderr = %q, want 1 and RRSet not found", code, stderr)
	}
}

func TestZoneNotFound(t *testing.T) {
	for _, args := range [][]string{
		{"set", "other.org", "_acme-challenge.other.org", "x"},
		{"unset", "other.org", "_acme-challenge.other.org", "x"},
		{"test", "other.org"},
	} {
		t.Run(args[0], func(t *testing.T) {
			code, _, stderr := runCmd(newFakeHetzner(t), args...)
			if code != 1 || !strings.Contains(stderr, "zone 'other.org' not found") {
				t.Errorf("exit code = %d, stderr = %q, want 1 and zone not found", code, stderr)
			}
		})
	}
}

func TestMissingToken(t *testing.T) {
	tests := []struct {
		args    []string
		wantErr string
	}{
		{[]string{"set", "example.com", "_acme-challenge.example.com", "x"}, "failed to get token"},
		{[]string{"unset", "example.com", "_acme-challenge.example.com", "x"}, "failed to get token"},
		{[]string{"test", "example.com"}, "failed to test token"},
	}

	for _, tt := range tests {
		t.Run(tt.args[0], func(t *testing.T) {
			f := newFakeHetzner(t)
			a, _, stderr := f.app()
			a.tokenPath = filepath.Join(t.TempDir(), "missing")

			if code := run(append([]string{"tah"}, tt.args...), a); code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("stderr = %q, want %q", stderr.String(), tt.wantErr)
			}
		})
	}
}

func TestAPIErrors(t *testing.T) {
	const rrset = "/zones/42/rrsets/_acme-challenge/TXT"
	tests := []struct {
		name     string
		existing bool
		args     []string
		fail     string
		wantErr  string
	}{
		{"set zone lookup", false, []string{"set"}, "GET /zones/example.com", "failed to get zone"},
		{"set rrset lookup", false, []string{"set"}, "GET " + rrset, "failed to get RRSet"},
		{"set create", false, []string{"set"}, "POST /zones/42/rrsets", "failed to create RRSet"},
		{"set add", true, []string{"set"}, "POST " + rrset + "/actions/add_records", "failed to add record to RRSet"},
		{"unset zone lookup", true, []string{"unset"}, "GET /zones/example.com", "failed to get zone"},
		{"unset rrset lookup", true, []string{"unset"}, "GET " + rrset, "failed to get RRSet"},
		{"unset remove", true, []string{"unset"}, "POST " + rrset + "/actions/remove_records", "failed to remove record"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeHetzner(t)
			if tt.existing {
				f.addRecords("_acme-challenge", `"x"`)
			}
			f.failOn(tt.fail, 1)

			args := append(tt.args, "example.com", "_acme-challenge.example.com", "x")
			code, _, stderr := runCmd(f, args...)
			if code != 1 || !strings.Contains(stderr, tt.wantErr) || !strings.Contains(stderr, "injected failure") {
				t.Errorf("exit code = %d, stderr = %q, want 1 and %q with the API message", code, stderr, tt.wantErr)
			}
		})
	}
}

func TestTestCommand(t *testing.T) {
	f := newFakeHetzner(t)

	code, stdout, stderr := runCmd(f, "test", "nas.example.com")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "Looking up zone: example.com") || !strings.Contains(stdout, "Test passed!") {
		t.Errorf("stdout = %q", stdout)
	}
	if _, ok := f.records(testRecordName); ok {
		t.Error("test record still exists after a passing test")
	}
	if !f.called("POST /zones/42/rrsets") || !f.called("DELETE /zones/42/rrsets/"+testRecordName+"/TXT") {
		t.Errorf("test must create and delete the record, calls %v", f.calls)
	}
}

func TestTestCommandRemovesLeftoverRecord(t *testing.T) {
	f := newFakeHetzner(t)
	f.addRecords(testRecordName, quoteTXT(testRecordText))

	code, _, stderr := runCmd(f, "test", "example.com")
	if code != 0 {
		t.Fatalf("a record left by an earlier crashed run must not break test: exit code = %d, stderr = %q", code, stderr)
	}
	if _, ok := f.records(testRecordName); ok {
		t.Error("test record still exists")
	}
}

func TestTestCommandFailures(t *testing.T) {
	rrset := "/zones/42/rrsets/" + testRecordName + "/TXT"
	tests := []struct {
		name        string
		leftover    bool
		fail        string
		n           int
		wantErr     string
		wantCleanup bool
	}{
		{"zone lookup", false, "GET /zones/example.com", 1, "failed to get zone", false},
		{"leftover lookup", false, "GET " + rrset, 1, "failed to remove leftover test record", false},
		{"leftover delete", true, "DELETE " + rrset, 1, "failed to remove leftover test record", false},
		{"create", false, "POST /zones/42/rrsets", 1, "failed to create test record", false},
		{"lookup after create", false, "GET " + rrset, 2, "failed to get test RRSet", true},
		{"delete", false, "DELETE " + rrset, 1, "failed to delete test record", true},
		{"wait for delete", false, "GET /actions", 1, "failed to delete test record", true},
		{"wait for leftover delete", true, "GET /actions", 1, "failed to remove leftover test record", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeHetzner(t)
			if tt.leftover {
				f.addRecords(testRecordName, quoteTXT(testRecordText))
			}
			f.failOn(tt.fail, tt.n)

			code, stdout, stderr := runCmd(f, "test", "example.com")
			if code != 1 || !strings.Contains(stderr, tt.wantErr) {
				t.Errorf("exit code = %d, stderr = %q, want 1 and %q", code, stderr, tt.wantErr)
			}
			if strings.Contains(stdout, "Test passed!") {
				t.Error("failed test must not print Test passed!")
			}
			if tt.wantCleanup {
				if _, ok := f.records(testRecordName); ok {
					t.Error("cleanup after a failure must remove the test record")
				}
			}
		})
	}
}

func TestInit(t *testing.T) {
	f := newFakeHetzner(t)
	a, stdout, stderr := f.app()
	a.tokenPath = filepath.Join(t.TempDir(), ".tahtoken")

	if code := run([]string{"tah", "init"}, a); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if _, err := os.Stat(a.tokenPath); err != nil {
		t.Errorf("token file not created: %v", err)
	}
	if !strings.Contains(stdout.String(), "Creating "+a.tokenPath) {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestInitKeepsExistingToken(t *testing.T) {
	f := newFakeHetzner(t)
	a, _, stderr := f.app()

	if code := run([]string{"tah", "init"}, a); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	token, err := readToken(a.tokenPath)
	if err != nil || token != fakeToken {
		t.Errorf("init must not truncate an existing token file: got %q, %v", token, err)
	}
}

func TestInitFails(t *testing.T) {
	f := newFakeHetzner(t)
	a, _, stderr := f.app()
	a.tokenPath = filepath.Join(t.TempDir(), "no-such-dir", ".tahtoken")

	if code := run([]string{"tah", "init"}, a); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "failed to create") {
		t.Errorf("stderr = %q, want failed to create", stderr.String())
	}
}
