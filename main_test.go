package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

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
	for _, want := range []string{
		"\ttah set|unset <domain> <validation_name> <validation_context>",
		"\ttah test <domain>",
		"\ttah init | version | --version | help",
		"\tset ", "\tunset ", "\tinit ", "\ttest ", "\tversion, --version ", "\thelp ",
	} {
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
	tests := []struct {
		args  []string
		tried string
	}{
		{[]string{"set", "other.org", "_acme-challenge.nas.other.org", "x"}, "(tried nas.other.org, other.org)"},
		{[]string{"unset", "other.org", "_acme-challenge.other.org", "x"}, "(tried other.org)"},
		{[]string{"test", "nas.other.org"}, "(tried nas.other.org, other.org)"},
	}
	for _, tt := range tests {
		t.Run(tt.args[0], func(t *testing.T) {
			f := newFakeHetzner(t)
			code, _, stderr := runCmd(f, tt.args...)
			if code != 1 || !strings.Contains(stderr, "no zone in the Hetzner account matches "+tt.tried) {
				t.Errorf("exit code = %d, stderr = %q, want 1 and the names tried %s", code, stderr, tt.tried)
			}
			// A missing zone is final, not a transient failure
			if strings.Contains(stderr, "Attempt") {
				t.Errorf("stderr = %q, want no retry", stderr)
			}
		})
	}
}

func TestZoneCandidates(t *testing.T) {
	tests := []struct {
		name string
		self bool
		want []string
	}{
		{"_acme-challenge.example.com", false, []string{"example.com"}},
		{"_acme-challenge.nas.dev.example.com", false, []string{"nas.dev.example.com", "dev.example.com", "example.com"}},
		{"_acme-challenge.Nas.Example.COM.", false, []string{"nas.example.com", "example.com"}},
		{"_acme-challenge.a.example.co.uk", false, []string{"a.example.co.uk", "example.co.uk"}},
		{"example.com", false, nil},
		{"example.com", true, []string{"example.com"}},
		{"nas.example.com.", true, []string{"nas.example.com", "example.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := zoneCandidates(tt.name, tt.self)
			if !ok || !slices.Equal(got, tt.want) {
				t.Errorf("zoneCandidates(%q, %v) = %v, %v, want %v", tt.name, tt.self, got, ok, tt.want)
			}
		})
	}

	for _, name := range []string{"com", "co.uk", "localhost", ""} {
		if got, ok := zoneCandidates(name, true); ok {
			t.Errorf("zoneCandidates(%q) = %v, want no registrable domain", name, got)
		}
	}
}

func TestInvalidValidationName(t *testing.T) {
	names := []string{
		"example.com", "com", "localhost",
		// Characters that would change the unescaped API path (#49)
		"_acme-challenge.www/A#.example.com",
		"_acme-challenge.www?x.example.com",
		"_acme-challenge#.example.com",
		"_acme challenge.example.com",
		"_acme-challenge..example.com",
	}
	for _, cmd := range []string{"set", "unset"} {
		for _, name := range names {
			t.Run(cmd+" "+name, func(t *testing.T) {
				f := newFakeHetzner(t)
				code, stdout, stderr := runCmd(f, cmd, "example.com", name, "x")
				if code != 1 || !strings.Contains(stderr, "Invalid validation_name: "+name+"!") || !strings.Contains(stdout, "help") {
					t.Errorf("exit code = %d, stderr = %q, want 1 and the usage error", code, stderr)
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				if len(f.calls) != 0 {
					t.Errorf("API calls %v, want none for an invalid validation_name", f.calls)
				}
			})
		}
	}
}

// Upper case, a trailing dot, digits, - and _ are valid in a validation_name.
func TestHostLabels(t *testing.T) {
	for _, name := range []string{"_acme-challenge.nas-01.example.com", "_acme-challenge.xn--bcher-kva.example"} {
		if !hostLabels(name) {
			t.Errorf("hostLabels(%q) = false, want true", name)
		}
	}
	f := newFakeHetzner(t)
	if code, _, stderr := runCmd(f, "set", "example.com", "_ACME-Challenge.Example.COM.", "x"); code != 0 {
		t.Errorf("exit code = %d, stderr = %q, want 0 for upper case and a trailing dot", code, stderr)
	}
	if _, ok := f.records("_acme-challenge"); !ok {
		t.Error("record _acme-challenge not created")
	}
}

// The record goes to the most specific zone in the account that contains
// validation_name, independent of <domain>.
func TestZoneLookup(t *testing.T) {
	tests := []struct {
		name       string
		zones      []string // added next to example.com
		domain     string
		validation string
		wantZone   string
		wantRecord string
	}{
		{"apex zone", nil, "example.com", "_acme-challenge.example.com", "example.com", "_acme-challenge"},
		{"host under apex zone", nil, "nas.example.com", "_acme-challenge.nas.example.com", "example.com", "_acme-challenge.nas"},
		{"subdomain zone", []string{"dev.example.com"}, "nas.dev.example.com", "_acme-challenge.nas.dev.example.com",
			"dev.example.com", "_acme-challenge.nas"},
		{"subdomain zone is not used for a sibling", []string{"dev.example.com"}, "nas.example.com", "_acme-challenge.nas.example.com",
			"example.com", "_acme-challenge.nas"},
		{"delegated challenge in another zone", []string{"acme.org"}, "nas.example.com", "_acme-challenge.nas.acme.org",
			"acme.org", "_acme-challenge.nas"},
		{"upper case and trailing dot", []string{"dev.example.com"}, "NAS.Dev.Example.com.", "_acme-challenge.NAS.Dev.Example.com.",
			"dev.example.com", "_acme-challenge.nas"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeHetzner(t)
			ids := map[string]int64{fakeZone: fakeZoneID}
			for _, z := range tt.zones {
				ids[z] = f.addZone(z)
			}

			code, stdout, stderr := runCmd(f, "set", tt.domain, tt.validation, "x")
			if code != 0 {
				t.Fatalf("set: exit code = %d, stderr = %q", code, stderr)
			}
			if want := "Zone " + tt.wantZone + ", record " + tt.wantRecord; !strings.Contains(stdout, want) {
				t.Errorf("stdout = %q, want %q", stdout, want)
			}
			got, ok := f.recordsIn(ids[tt.wantZone], tt.wantRecord)
			if !ok || !slices.Equal(got, []string{`"x"`}) || f.rrsetCount() != 1 {
				t.Errorf("records in %s/%s = %v (exists %v), %d RRSets in total, want only this one",
					tt.wantZone, tt.wantRecord, got, ok, f.rrsetCount())
			}

			if code, _, stderr := runCmd(f, "unset", tt.domain, tt.validation, "x"); code != 0 {
				t.Fatalf("unset: exit code = %d, stderr = %q", code, stderr)
			}
			if f.rrsetCount() != 0 {
				t.Errorf("%d RRSets left after unset, want 0", f.rrsetCount())
			}
			assertAllWaited(t, f)
		})
	}
}

// A transient failure of one of the zone lookups reruns the whole lookup.
func TestZoneLookupRetry(t *testing.T) {
	f := newFakeHetzner(t)
	f.failWith("GET /zones/nas.example.com", failure{n: 1, status: 503, code: "service_error"})

	code, _, stderr := runCmd(f, "set", "nas.example.com", "_acme-challenge.nas.example.com", "x")
	if code != 0 || !strings.Contains(stderr, "Attempt 1 of 3 failed") || !strings.Contains(stderr, "failed to get zone nas.example.com") {
		t.Errorf("exit code = %d, stderr = %q, want 0 after a retry of the zone lookup", code, stderr)
	}
	if got, ok := f.records("_acme-challenge.nas"); !ok || !slices.Equal(got, []string{`"x"`}) {
		t.Errorf("records = %v (exists %v)", got, ok)
	}
}

func TestTestCommandInSubdomainZone(t *testing.T) {
	f := newFakeHetzner(t)
	id := f.addZone("dev.example.com")

	code, stdout, stderr := runCmd(f, "test", "nas.dev.example.com")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "Found zone: dev.example.com") {
		t.Errorf("stdout = %q, want the subdomain zone", stdout)
	}
	create := "POST /zones/" + strconv.FormatInt(id, 10) + "/rrsets"
	if !f.called(create) {
		t.Errorf("test record not created in dev.example.com, calls %v", f.calls)
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
			hint := "token file " + a.tokenPath + ` not found. Run "tah init" as the Running User, then put the token into it`
			if !strings.Contains(stderr.String(), tt.wantErr) || !strings.Contains(stderr.String(), hint) {
				t.Errorf("stderr = %q, want %q and %q", stderr.String(), tt.wantErr, hint)
			}
			if f.rrsetCount() != 0 || f.called("GET /zones/example.com") {
				t.Error("a missing token file must stop before any API call")
			}
		})
	}
}

// A rejected token gets the hint about the token type, whichever call it
// fails on; other API errors do not.
func TestTokenRejectedHint(t *testing.T) {
	tests := []struct {
		name     string
		call     string
		fail     failure
		wantHint bool
	}{
		{"401 on zone lookup", "GET /zones/example.com", failure{status: 401, code: "unauthorized"}, true},
		{"403 on create", "POST /zones/42/rrsets", failure{status: 403, code: "forbidden"}, true},
		{"422 on create", "POST /zones/42/rrsets", failure{status: 422, code: "invalid_input"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeHetzner(t)
			f.failWith(tt.call, tt.fail)
			code, _, stderr := runCmd(f, "set", "example.com", "_acme-challenge.example.com", "x")
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if got := strings.Contains(stderr, tokenRejectedHint); got != tt.wantHint {
				t.Errorf("stderr = %q, want the token hint: %v", stderr, tt.wantHint)
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
	if !strings.Contains(stdout, "Looking up zone for: nas.example.com") || !strings.Contains(stdout, "Found zone: example.com") ||
		!strings.Contains(stdout, "Test passed!") {
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

func TestReadTokenTrimsWhitespace(t *testing.T) {
	tests := map[string]string{
		"LF":                fakeToken + "\n",
		"CRLF":              fakeToken + "\r\n",
		"no newline":        fakeToken,
		"spaces":            "  " + fakeToken + "  \n",
		"tabs":              "\t" + fakeToken + "\t\r\n",
		"trailing new line": fakeToken + "\n\n",
	}

	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".tahtoken")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			token, err := readToken(path)
			if err != nil || token != fakeToken {
				t.Errorf("readToken(%q) = %q, %v, want %q", content, token, err, fakeToken)
			}
		})
	}
}

// A token file with CRLF line endings authenticates end to end; the fake
// rejects any Authorization header other than the bare token.
func TestSetWithCRLFToken(t *testing.T) {
	f := newFakeHetzner(t)
	a, _, stderr := f.app()
	if err := os.WriteFile(a.tokenPath, []byte(fakeToken+"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if code := run([]string{"tah", "set", "example.com", "_acme-challenge.example.com", "x"}, a); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
}

func TestEmptyToken(t *testing.T) {
	commands := [][]string{
		{"set", "example.com", "_acme-challenge.example.com", "x"},
		{"unset", "example.com", "_acme-challenge.example.com", "x"},
		{"test", "example.com"},
	}
	contents := map[string]string{
		"empty":           "",
		"only whitespace": " \t\r\n\n",
	}

	for _, args := range commands {
		for name, content := range contents {
			t.Run(args[0]+"/"+name, func(t *testing.T) {
				f := newFakeHetzner(t)
				a, _, stderr := f.app()
				if err := os.WriteFile(a.tokenPath, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}

				if code := run(append([]string{"tah"}, args...), a); code != 1 {
					t.Errorf("exit code = %d, want 1", code)
				}
				want := "token file " + a.tokenPath + " is empty, put your Hetzner Cloud API token in it"
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr = %q, want %q", stderr.String(), want)
				}
				f.mu.Lock()
				calls := slices.Clone(f.calls)
				f.mu.Unlock()
				if len(calls) != 0 {
					t.Errorf("API calls with an empty token: %v", calls)
				}
			})
		}
	}
}

func TestUserAgent(t *testing.T) {
	old := Version
	Version = "v9.9.9"
	t.Cleanup(func() { Version = old })

	f := newFakeHetzner(t)
	if code, _, stderr := runCmd(f, "set", "example.com", "_acme-challenge.example.com", "x"); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	f.mu.Lock()
	got := f.userAgent
	f.mu.Unlock()
	if want := "truenas-acme-hetzner/v9.9.9 hcloud-go/"; !strings.HasPrefix(got, want) {
		t.Errorf("User-Agent = %q, want prefix %q", got, want)
	}
}

func TestNewClient(t *testing.T) {
	if newClient(fakeToken) == nil {
		t.Fatal("newClient returned nil")
	}
}

// Transient failures are retried from the GET, and the command ends as if
// nothing had happened.
func TestRetryTransientFailures(t *testing.T) {
	const rrset = "/zones/42/rrsets/_acme-challenge/TXT"
	tests := []struct {
		name     string
		existing []string
		cmd      string
		call     string
		fail     failure
		want     []string // records afterwards; nil means the RRSet is gone
		// The rerun finds the change in place and does not know the action of
		// the first attempt, so that action stays unpolled. The nameserver
		// check of #21 covers a change that is not applied yet.
		unwaited bool
	}{
		{"500 on rrset lookup", nil, "set", "GET " + rrset,
			failure{n: 1, status: 500, code: "server_error"}, []string{`"x"`}, false},
		{"503 without API body on create", nil, "set", "POST /zones/42/rrsets",
			failure{n: 1, status: 503}, []string{`"x"`}, false},
		{"connection reset on create", nil, "set", "POST /zones/42/rrsets",
			failure{n: 1, drop: dropBefore}, []string{`"x"`}, false},
		{"lost response after create", nil, "set", "POST /zones/42/rrsets",
			failure{n: 1, drop: dropAfter}, []string{`"x"`}, true},
		{"502 on wait for add", []string{`"other"`}, "set", "GET /actions",
			failure{n: 1, status: 502}, []string{`"other"`, `"x"`}, true},
		{"rate limit on add", []string{`"other"`}, "set", "POST " + rrset + "/actions/add_records",
			failure{n: 1, status: 429, code: "rate_limit_exceeded"}, []string{`"other"`, `"x"`}, false},
		{"503 on remove", []string{`"x"`, `"other"`}, "unset", "POST " + rrset + "/actions/remove_records",
			failure{n: 1, status: 503, code: "service_error"}, []string{`"other"`}, false},
		{"connection reset on delete", []string{`"x"`}, "unset", "DELETE " + rrset,
			failure{n: 1, drop: dropBefore}, nil, false},
		{"lost response after delete", []string{`"x"`}, "unset", "DELETE " + rrset,
			failure{n: 1, drop: dropAfter}, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeHetzner(t)
			if tt.existing != nil {
				f.addRecords("_acme-challenge", tt.existing...)
			}
			f.failWith(tt.call, tt.fail)

			code, _, stderr := runCmd(f, tt.cmd, "example.com", "_acme-challenge.example.com", "x")
			if code != 0 {
				t.Fatalf("exit code = %d, stderr = %q, want 0 after a retry", code, stderr)
			}
			if !strings.Contains(stderr, "Attempt 1 of 3 failed") {
				t.Errorf("stderr = %q, want the failed first attempt", stderr)
			}
			got, ok := f.records("_acme-challenge")
			if tt.want == nil && ok {
				t.Errorf("RRSet still exists with %v", got)
			}
			if tt.want != nil && !slices.Equal(got, tt.want) {
				t.Errorf("records = %v, want %v", got, tt.want)
			}
			if !tt.unwaited {
				assertAllWaited(t, f)
			}
		})
	}
}

// A change that Hetzner applied but whose answer timed out is sent exactly
// once: the retry reads the record set again and finds the change in place,
// instead of resending the request blind (#50). A blind resend fails unset
// with 404 after the delete or stores an added value twice.
func TestAppliedChangeWithTimedOutAnswerIsNotResent(t *testing.T) {
	const rrset = "/zones/42/rrsets/_acme-challenge/TXT"
	tests := []struct {
		name     string
		existing []string
		cmd      string
		call     string
		want     []string // records afterwards; nil means the RRSet is gone
	}{
		{"create", nil, "set", "POST /zones/42/rrsets", []string{`"x"`}},
		{"add", []string{`"other"`}, "set", "POST " + rrset + "/actions/add_records", []string{`"other"`, `"x"`}},
		{"remove", []string{`"x"`, `"other"`}, "unset", "POST " + rrset + "/actions/remove_records", []string{`"other"`}},
		{"delete", []string{`"x"`}, "unset", "DELETE " + rrset, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeHetzner(t)
			f.requestTimeout = 100 * time.Millisecond
			if tt.existing != nil {
				f.addRecords("_acme-challenge", tt.existing...)
			}
			f.failWith(tt.call, failure{n: 1, drop: timeoutAfter})

			code, _, stderr := runCmd(f, tt.cmd, "example.com", "_acme-challenge.example.com", "x")
			if code != 0 {
				t.Fatalf("exit code = %d, stderr = %q, want 0", code, stderr)
			}
			if n := f.count(tt.call); n != 1 {
				t.Errorf("%s was sent %d times, want 1", tt.call, n)
			}
			if !strings.Contains(stderr, "Attempt 1 of 3 failed") {
				t.Errorf("stderr = %q, want the timed out first attempt", stderr)
			}
			got, ok := f.records("_acme-challenge")
			if tt.want == nil && ok {
				t.Errorf("RRSet still exists with %v", got)
			}
			if tt.want != nil && !slices.Equal(got, tt.want) {
				t.Errorf("records = %v, want %v", got, tt.want)
			}
			assertAllWaited(t, f)
		})
	}
}

// Authentication, not found and validation errors are final.
func TestNotRetried(t *testing.T) {
	const rrset = "/zones/42/rrsets/_acme-challenge/TXT"
	tests := []struct {
		name     string
		existing []string
		cmd      string
		call     string
		fail     failure
	}{
		{"401 on zone lookup", nil, "set", "GET /zones/example.com",
			failure{status: 401, code: "unauthorized"}},
		{"403 on create", nil, "set", "POST /zones/42/rrsets",
			failure{status: 403, code: "forbidden"}},
		{"404 on delete", []string{`"x"`}, "unset", "DELETE " + rrset,
			failure{status: 404, code: "not_found"}},
		{"422 on add", []string{`"other"`}, "set", "POST " + rrset + "/actions/add_records",
			failure{status: 422, code: "invalid_input"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeHetzner(t)
			if tt.existing != nil {
				f.addRecords("_acme-challenge", tt.existing...)
			}
			f.failWith(tt.call, tt.fail)

			code, _, stderr := runCmd(f, tt.cmd, "example.com", "_acme-challenge.example.com", "x")
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if n := f.count(tt.call); n != 1 {
				t.Errorf("%s was called %d times, want 1", tt.call, n)
			}
			if strings.Contains(stderr, "Attempt") {
				t.Errorf("stderr = %q, want no retry", stderr)
			}
		})
	}
}

func TestRetryGivesUpAfterAllAttempts(t *testing.T) {
	const call = "GET /zones/42/rrsets/_acme-challenge/TXT"
	f := newFakeHetzner(t)
	f.failWith(call, failure{status: 503, code: "service_error"})

	code, _, stderr := runCmd(f, "set", "example.com", "_acme-challenge.example.com", "x")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if n := f.count(call); n != retryAttempts {
		t.Errorf("%s was called %d times, want %d", call, n, retryAttempts)
	}
	if want := "failed to get RRSet: "; !strings.Contains(stderr, want) || !strings.HasSuffix(strings.TrimSpace(stderr), "(after 3 attempts)") {
		t.Errorf("stderr = %q, want %q and the attempt count", stderr, want)
	}
}

// An API that keeps failing must not hold the command past the retry budget,
// even when the backoff would wait longer.
func TestRetryBudget(t *testing.T) {
	f := newFakeHetzner(t)
	f.failWith("GET /zones/example.com", failure{status: 503})
	a, _, stderr := f.app()
	a.retryBudget = 50 * time.Millisecond
	a.retryDelay = time.Hour

	start := time.Now()
	code := run([]string{"tah", "set", "example.com", "_acme-challenge.example.com", "x"}, a)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("command took %s, want it to stop at the retry budget", elapsed)
	}
	if code != 1 || !strings.Contains(stderr.String(), "gave up after 50ms, attempts: 1") {
		t.Errorf("exit code = %d, stderr = %q, want 1 and the used up retry budget", code, stderr.String())
	}
}

// A request that hangs uses up the retry budget, not tah's whole timeout.
func TestRetryBudgetHangingRequest(t *testing.T) {
	f := newFakeHetzner(t)
	f.hangOn = "GET /zones/example.com"
	a, _, stderr := f.app()
	a.retryBudget = 50 * time.Millisecond

	code := run([]string{"tah", "unset", "example.com", "_acme-challenge.example.com", "x"}, a)
	if code != 1 || !strings.Contains(stderr.String(), "gave up after 50ms, attempts: 1") {
		t.Errorf("exit code = %d, stderr = %q, want 1 and the used up retry budget", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "timed out after") {
		t.Errorf("stderr = %q, the retry budget must end before the command timeout", stderr.String())
	}
}

// The binary runs with the production limits, not with zero values that
// would disable the retries or end every command at once.
func TestDefaultApp(t *testing.T) {
	t.Setenv("HOME", "/home/acme")
	a := defaultApp()

	if a.stdout != os.Stdout || a.stderr != os.Stderr {
		t.Error("defaultApp must write to the terminal")
	}
	if want := "/home/acme/.tahtoken"; a.tokenPath != want {
		t.Errorf("tokenPath = %q, want %q", a.tokenPath, want)
	}
	if a.newClient == nil {
		t.Error("newClient is nil")
	}
	if a.timeout != apiTimeout || a.retryBudget != retryBudget || a.retryDelay != retryDelay {
		t.Errorf("timeout %s, retryBudget %s, retryDelay %s, want %s, %s, %s",
			a.timeout, a.retryBudget, a.retryDelay, apiTimeout, retryBudget, retryDelay)
	}
}

// The retries must end before tah's own timeout, which ends before the
// TrueNAS Script Timeout, so the error comes from tah and not from a kill.
func TestRetryBudgetFitsTimeout(t *testing.T) {
	// Worst case of the backoff between the command attempts alone
	var backoff time.Duration
	for i := range retryAttempts - 1 {
		backoff += retryDelay << i
	}
	if retryBudget >= apiTimeout || backoff >= retryBudget {
		t.Errorf("retryBudget %s, backoff %s, apiTimeout %s: want backoff < retryBudget < apiTimeout", retryBudget, backoff, apiTimeout)
	}
}

// Two certificate jobs for the same name: both see no RRSet, one create loses
// with "already exists" and is rerun, then adds its value to the other's RRSet.
func TestConcurrentSetSameName(t *testing.T) {
	f := newFakeHetzner(t)
	values := []string{"first", "second"}

	var wg sync.WaitGroup
	results := make([]string, len(values))
	for i, v := range values {
		a, _, stderr := f.app()
		wg.Go(func() {
			if code := run([]string{"tah", "set", "example.com", "_acme-challenge.example.com", v}, a); code != 0 {
				results[i] = fmt.Sprintf("set %s: exit code %d, stderr %q", v, code, stderr.String())
			}
		})
	}
	wg.Wait()

	for _, r := range results {
		if r != "" {
			t.Error(r)
		}
	}
	got, _ := f.records("_acme-challenge")
	slices.Sort(got)
	if want := []string{`"first"`, `"second"`}; !slices.Equal(got, want) {
		t.Errorf("records = %v, want %v", got, want)
	}
	assertAllWaited(t, f)
}
