package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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
	if req.Type != "TXT" || req.TTL == nil || *req.TTL != 60 {
		t.Errorf("create request type = %q ttl = %v, want TXT and 60", req.Type, req.TTL)
	}
	assertAllWaited(t, f)
}

// assertAllWaited fails if a command returned without waiting for an action it started.
func assertAllWaited(t *testing.T, f *fakeHetzner) {
	t.Helper()
	if ids := f.unfinished(); len(ids) > 0 {
		t.Errorf("actions %v were never waited for", ids)
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
	assertAllWaited(t, f)
}

// A certificate for example.com and *.example.com puts two values under the
// same name, one set call right after the other. The second call only gets
// through if the first one waited until its RRSet was no longer locked.
func TestSetTwoValuesForTheSameName(t *testing.T) {
	f := newFakeHetzner(t)

	for _, value := range []string{"apex", "wildcard"} {
		code, _, stderr := runCmd(f, "set", "example.com", "_acme-challenge.example.com", value)
		if code != 0 {
			t.Fatalf("set %s: exit code = %d, stderr = %q", value, code, stderr)
		}
	}

	got, _ := f.records("_acme-challenge")
	if !slices.Equal(got, []string{`"apex"`, `"wildcard"`}) {
		t.Errorf("records = %v, want both values", got)
	}
	assertAllWaited(t, f)
}

func TestSetValueAlreadyPresent(t *testing.T) {
	f := newFakeHetzner(t)
	f.addRecords("_acme-challenge", `"token-value"`)

	code, stdout, stderr := runCmd(f, "set", "example.com", "_acme-challenge.example.com", "token-value")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "Record already present") {
		t.Errorf("stdout = %q, want the already present note", stdout)
	}

	got, _ := f.records("_acme-challenge")
	if !slices.Equal(got, []string{`"token-value"`}) {
		t.Errorf("records = %v, want the value exactly once", got)
	}
	if f.called("POST /zones/42/rrsets/_acme-challenge/TXT/actions/add_records") {
		t.Error("set must not add a value that is already present")
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
	assertAllWaited(t, f)
}

func TestUnsetDeletesRRSetWithLastRecord(t *testing.T) {
	f := newFakeHetzner(t)
	f.addRecords("_acme-challenge", `"drop"`)

	code, _, stderr := runCmd(f, "unset", "example.com", "_acme-challenge.example.com", "drop")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}

	if got, ok := f.records("_acme-challenge"); ok {
		t.Errorf("RRSet still exists with %v, want it deleted", got)
	}
	if f.called("POST /zones/42/rrsets/_acme-challenge/TXT/actions/remove_records") {
		t.Error("the last record must be removed by deleting the RRSet")
	}
	assertAllWaited(t, f)
}

func TestUnsetNothingToRemove(t *testing.T) {
	tests := []struct {
		name     string
		existing []string
	}{
		{"RRSet missing", nil},
		{"value missing", []string{`"other"`}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeHetzner(t)
			if tt.existing != nil {
				f.addRecords("_acme-challenge", tt.existing...)
			}

			code, stdout, stderr := runCmd(f, "unset", "example.com", "_acme-challenge.example.com", "x")
			if code != 0 {
				t.Fatalf("exit code = %d, stderr = %q, want 0 for a value that is already gone", code, stderr)
			}
			if !strings.Contains(stdout, "Record not found, nothing to remove") {
				t.Errorf("stdout = %q, want the nothing to remove note", stdout)
			}
			for _, v := range tt.existing {
				if !strings.Contains(stdout, v) {
					t.Errorf("stdout = %q, want the stored value %s for diagnosis", stdout, v)
				}
			}
			got, _ := f.records("_acme-challenge")
			if !slices.Equal(got, tt.existing) {
				t.Errorf("records = %v, want unchanged %v", got, tt.existing)
			}
		})
	}
}

func TestUnsetTwice(t *testing.T) {
	f := newFakeHetzner(t)
	f.addRecords("_acme-challenge", `"x"`)

	for i := 1; i <= 2; i++ {
		code, _, stderr := runCmd(f, "unset", "example.com", "_acme-challenge.example.com", "x")
		if code != 0 {
			t.Fatalf("unset run %d: exit code = %d, stderr = %q", i, code, stderr)
		}
	}
	if _, ok := f.records("_acme-challenge"); ok {
		t.Error("RRSet still exists")
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
	var (
		none     []string
		other    = []string{`"other"`}
		only     = []string{`"x"`}
		withMore = []string{`"x"`, `"other"`}
	)
	tests := []struct {
		name     string
		existing []string
		cmd      string
		fail     string
		wantErr  string
	}{
		{"set zone lookup", none, "set", "GET /zones/example.com", "failed to get zone"},
		{"set rrset lookup", none, "set", "GET " + rrset, "failed to get RRSet"},
		{"set create", none, "set", "POST /zones/42/rrsets", "failed to create RRSet"},
		{"set wait for create", none, "set", "GET /actions", "failed to create RRSet"},
		{"set add", other, "set", "POST " + rrset + "/actions/add_records", "failed to add record to RRSet"},
		{"set wait for add", other, "set", "GET /actions", "failed to add record to RRSet"},
		{"unset zone lookup", withMore, "unset", "GET /zones/example.com", "failed to get zone"},
		{"unset rrset lookup", withMore, "unset", "GET " + rrset, "failed to get RRSet"},
		{"unset remove", withMore, "unset", "POST " + rrset + "/actions/remove_records", "failed to remove record"},
		{"unset wait for remove", withMore, "unset", "GET /actions", "failed to remove record"},
		{"unset delete last record", only, "unset", "DELETE " + rrset, "failed to delete RRSet"},
		{"unset wait for delete", only, "unset", "GET /actions", "failed to delete RRSet"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeHetzner(t)
			if tt.existing != nil {
				f.addRecords("_acme-challenge", tt.existing...)
			}
			f.failOn(tt.fail, 1)

			code, _, stderr := runCmd(f, tt.cmd, "example.com", "_acme-challenge.example.com", "x")
			if code != 1 || !strings.Contains(stderr, tt.wantErr) || !strings.Contains(stderr, "injected failure") {
				t.Errorf("exit code = %d, stderr = %q, want 1 and %q with the API message", code, stderr, tt.wantErr)
			}
		})
	}
}

// An action the API accepted can still fail; the command must report it, not exit 0.
func TestActionFailures(t *testing.T) {
	tests := []struct {
		name     string
		existing []string
		args     []string
		wantErr  string
	}{
		{"set create", nil, []string{"set", "example.com", "_acme-challenge.example.com", "x"}, "failed to create RRSet"},
		{"set add", []string{`"other"`}, []string{"set", "example.com", "_acme-challenge.example.com", "x"}, "failed to add record to RRSet"},
		{"unset remove", []string{`"x"`, `"other"`}, []string{"unset", "example.com", "_acme-challenge.example.com", "x"}, "failed to remove record"},
		{"unset delete", []string{`"x"`}, []string{"unset", "example.com", "_acme-challenge.example.com", "x"}, "failed to delete RRSet"},
		{"test create", nil, []string{"test", "example.com"}, "failed to create test record"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeHetzner(t)
			if tt.existing != nil {
				f.addRecords("_acme-challenge", tt.existing...)
			}
			f.failActions = true

			code, _, stderr := runCmd(f, tt.args...)
			if code != 1 || !strings.Contains(stderr, tt.wantErr) || !strings.Contains(stderr, "injected action failure") {
				t.Errorf("exit code = %d, stderr = %q, want 1 and %q with the action error", code, stderr, tt.wantErr)
			}
		})
	}
}

func TestTimeout(t *testing.T) {
	tests := []struct {
		name string
		hang string
		args []string
	}{
		{"set zone lookup", "GET /zones/example.com", []string{"set", "example.com", "_acme-challenge.example.com", "x"}},
		{"set wait for action", "GET /actions", []string{"set", "example.com", "_acme-challenge.example.com", "x"}},
		{"unset zone lookup", "GET /zones/example.com", []string{"unset", "example.com", "_acme-challenge.example.com", "x"}},
		{"test zone lookup", "GET /zones/example.com", []string{"test", "example.com"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := tt.args
			f := newFakeHetzner(t)
			f.hangOn = tt.hang
			a, _, stderr := f.app()
			a.timeout = 50 * time.Millisecond

			start := time.Now()
			code := run(append([]string{"tah"}, args...), a)
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Errorf("command took %s, want it to give up after the timeout", elapsed)
			}
			if code != 1 || !strings.Contains(stderr.String(), "timed out after 50ms") {
				t.Errorf("exit code = %d, stderr = %q, want 1 and the timeout", code, stderr.String())
			}
		})
	}
}

// After a timeout the deadline of the command is used up; the cleanup of the
// test record must still run on its own deadline.
func TestTestCommandCleansUpAfterTimeout(t *testing.T) {
	f := newFakeHetzner(t)
	f.hangOn = "GET /actions"
	a, _, stderr := f.app()
	a.timeout = 50 * time.Millisecond

	if code := run([]string{"tah", "test", "example.com"}, a); code != 1 {
		t.Fatalf("exit code = %d, stderr = %q, want 1", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "timed out after 50ms") {
		t.Errorf("stderr = %q, want the timeout", stderr.String())
	}
	if _, ok := f.records(testRecordName); ok {
		t.Error("test record still exists after the timeout")
	}
}

// The API may store a value in another form than quoteTXT produces, for
// example split into several strings. Matching works on the parsed value, and
// removal sends the stored form back.
func TestStoredValueInAnotherForm(t *testing.T) {
	const stored = `"tok" "en"`

	t.Run("set", func(t *testing.T) {
		f := newFakeHetzner(t)
		f.addRecords("_acme-challenge", stored)

		code, stdout, stderr := runCmd(f, "set", "example.com", "_acme-challenge.example.com", "token")
		if code != 0 || !strings.Contains(stdout, "Record already present") {
			t.Fatalf("exit code = %d, stdout = %q, stderr = %q, want 0 and already present", code, stdout, stderr)
		}
		if got, _ := f.records("_acme-challenge"); !slices.Equal(got, []string{stored}) {
			t.Errorf("records = %v, want only the stored value", got)
		}
	})

	t.Run("unset", func(t *testing.T) {
		f := newFakeHetzner(t)
		f.addRecords("_acme-challenge", stored, `"other"`)

		code, _, stderr := runCmd(f, "unset", "example.com", "_acme-challenge.example.com", "token")
		if code != 0 {
			t.Fatalf("exit code = %d, stderr = %q", code, stderr)
		}
		if got, _ := f.records("_acme-challenge"); !slices.Equal(got, []string{`"other"`}) {
			t.Errorf("records = %v, want only the other value left", got)
		}
	})
}

func TestQuoteTXT(t *testing.T) {
	tests := []struct{ in, want string }{
		{"abc-DEF_123", `"abc-DEF_123"`},
		{`say "hi"`, `"say \"hi\""`},
	}
	for _, tt := range tests {
		if got := quoteTXT(tt.in); got != tt.want {
			t.Errorf("quoteTXT(%q) = %q, want %q", tt.in, got, tt.want)
		}
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
	assertAllWaited(t, f)
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
		{"wait for create", false, "GET /actions", 1, "failed to create test record", true},
		{"delete", false, "DELETE " + rrset, 1, "failed to delete test record", true},
		{"wait for delete", false, "GET /actions", 2, "failed to delete test record", true},
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
