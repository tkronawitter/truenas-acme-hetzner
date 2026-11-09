package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/hetznercloud/hcloud-go/v2/hcloud/exp/zoneutil"
	"golang.org/x/net/publicsuffix"
)

// Version is set at build time: go build -ldflags "-X main.Version=v1.2.0"
var Version = "dev"

const (
	// Challenge records live for minutes. A short TTL keeps resolvers from
	// serving an old challenge value when TrueNAS retries soon after a failure.
	txtTTL = 60
	// apiTimeout bounds a whole command, including waiting for Hetzner actions,
	// so a stalled API call cannot block the TrueNAS certificate job. It stays
	// below the default TrueNAS "Script Timeout" of 60s, so tah reports the
	// timeout itself instead of being killed in the middle of a wait.
	apiTimeout = 50 * time.Second
	// retryBudget bounds set and unset including their retries. It leaves
	// apiTimeout-retryBudget for the nameserver check planned in #21.
	retryBudget = 40 * time.Second
	// retryAttempts is how often set and unset run before tah gives up, and
	// retryDelay the wait before the second run; it doubles after that.
	retryAttempts = 3
	retryDelay    = 1 * time.Second
	// requestTimeout ends a single API request that hangs, so a stalled
	// connection becomes a retry instead of using up the whole retryBudget.
	requestTimeout = 15 * time.Second
	// cleanupTimeout gives the test command's cleanup its own time budget,
	// because it also has to run after apiTimeout has expired.
	cleanupTimeout = 10 * time.Second
	testRecordName = "hcdcadclk"
	testRecordText = "TEST-TRUENAS-ACME-HETZNER"
)

const helpText = `Usage: %s <command> <domain> <validation_name> <validation_context>

Command:
	set
	unset
	init - Initialize script
	test - Test script configuration
	version, --version - Print version
	help - Print help
`

// app holds everything a command touches outside its arguments, so tests can
// swap the output streams, the token file, the Hetzner API endpoint, the
// timeout and the retry settings.
type app struct {
	stdout      io.Writer
	stderr      io.Writer
	tokenPath   string
	newClient   func(token string) *hcloud.Client
	timeout     time.Duration
	retryBudget time.Duration
	attempts    int
	retryDelay  time.Duration
	ns          nsCheck
}

// usageError marks invalid command line input; run prints the help hint after it.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

// printf writes to stdout. A failed write to the terminal is not actionable.
func (a app) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(a.stdout, format, args...)
}

// zoneCandidates returns the names that may be the Hetzner zone holding name,
// most specific first, down to the registrable domain (e.g. "example.com" or
// "example.co.uk"); public suffixes such as "com" are never candidates. With
// self, name itself is the first candidate. Names are compared in lower case
// without a trailing dot, the form Hetzner stores zone names in. ok is false
// for a name without a registrable domain.
func zoneCandidates(name string, self bool) (candidates []string, ok bool) {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	apex, err := publicsuffix.EffectiveTLDPlusOne(name)
	if err != nil {
		return nil, false
	}
	for c := name; ; {
		if c != name || self {
			candidates = append(candidates, c)
		}
		if c == apex {
			return candidates, true
		}
		_, c, _ = strings.Cut(c, ".")
	}
}

// domainArg returns the zone candidates for <domain>, the argument of test.
// set and unset only check that <domain> is valid; their zone comes from
// validation_name, which may lie in another zone (CNAME delegation).
func domainArg(args []string) ([]string, error) {
	if len(args) < 3 {
		return nil, usageError{"domain is missing!"}
	}
	candidates, ok := zoneCandidates(args[2], true)
	if !ok {
		return nil, usageError{fmt.Sprintf("Invalid domain: %s!", args[2])}
	}
	return candidates, nil
}

// recordArgs returns the validation name, the zone candidates for it and the
// TXT value for set and unset: <domain> <validation_name> <validation_context>.
// The validation name itself is not a candidate: a challenge record never sits
// at a zone apex, and a name with an underscore label is no valid zone name.
func recordArgs(args []string) (name string, candidates []string, value string, err error) {
	if _, err := domainArg(args); err != nil {
		return "", nil, "", err
	}
	if len(args) < 4 {
		return "", nil, "", usageError{"validation_name is missing!"}
	}
	if len(args) < 5 {
		return "", nil, "", usageError{"validation_context is missing!"}
	}
	candidates, ok := zoneCandidates(args[3], false)
	if !ok || len(candidates) == 0 {
		return "", nil, "", usageError{fmt.Sprintf("Invalid validation_name: %s!", args[3])}
	}
	return strings.ToLower(strings.TrimSuffix(args[3], ".")), candidates, args[4], nil
}

// rrsetName returns name relative to zone, e.g. "_acme-challenge.nas" for
// "_acme-challenge.nas.example.com" in example.com.
func rrsetName(name string, zone *hcloud.Zone) string {
	return strings.TrimSuffix(name, "."+strings.ToLower(zone.Name))
}

// quoteTXT quotes a TXT value as the Hetzner API expects it: quotes inside the
// value are escaped, and values over 255 characters are split into several
// quoted strings. ACME challenge values (43 base64url characters) only get the
// surrounding quotes.
func quoteTXT(value string) string {
	return zoneutil.FormatTXTRecord(value)
}

// findRecord returns the record of rrset that holds the unquoted value, in the
// form the API stores it. Values are compared after parsing, so a different
// quoting or chunking on the server side still matches.
func findRecord(rrset *hcloud.ZoneRRSet, value string) (hcloud.ZoneRRSetRecord, bool) {
	i := slices.IndexFunc(rrset.Records, func(r hcloud.ZoneRRSetRecord) bool {
		return r.Value == value || zoneutil.ParseTXTRecord(r.Value) == value
	})
	if i < 0 {
		return hcloud.ZoneRRSetRecord{}, false
	}
	return rrset.Records[i], true
}

// recordValues lists the stored values of rrset for diagnostic output.
func recordValues(rrset *hcloud.ZoneRRSet) []string {
	values := make([]string, 0, len(rrset.Records))
	for _, r := range rrset.Records {
		values = append(values, r.Value)
	}
	return values
}

// readToken returns the token without surrounding whitespace, so a file saved
// with CRLF line endings or a trailing space still authenticates. An empty
// file (the state right after init) fails here, before any API call.
func readToken(path string) (string, error) {
	out, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", fmt.Errorf("token file %s is empty, put your Hetzner Cloud API token in it", path)
	}
	return token, nil
}

// checkTokenFile returns warnings about the ownership and mode of the token file.
func checkTokenFile(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	warnings := ownerWarnings(info)
	if info.Mode() != 0o600 {
		warnings = append(warnings, fmt.Sprintf("Dangerous file mode: %o, use 0600!", info.Mode()))
	}
	return warnings, nil
}

// clientOptions are the options every API client gets. The application name
// puts "truenas-acme-hetzner/<Version>" in front of the hcloud-go User-Agent,
// so Hetzner can tell this tool apart from other hcloud-go clients.
//
// hcloud-go retries 502, 504, network timeouts and a few API error codes on
// its own, by default 5 times with up to 60s backoff. That alone could use up
// apiTimeout, and it would multiply with the command retries in withRetry, so
// it is cut down to 2 quick retries per request.
func clientOptions(token string) []hcloud.ClientOption {
	return []hcloud.ClientOption{
		hcloud.WithToken(token),
		hcloud.WithApplication("truenas-acme-hetzner", Version),
		hcloud.WithHTTPClient(&http.Client{Timeout: requestTimeout}),
		hcloud.WithRetryOpts(hcloud.RetryOpts{
			MaxRetries: 2,
			BackoffFunc: hcloud.ExponentialBackoffWithOpts(hcloud.ExponentialBackoffOpts{
				Base:       500 * time.Millisecond,
				Multiplier: 2,
				Cap:        2 * time.Second,
				Jitter:     true,
			}),
		}),
	}
}

// retryable reports whether err is worth running set or unset again: a server
// side or network failure, a request that collided with another change
// (conflict, locked, an RRSet that already exists), or rate limiting.
// Authentication, not found and validation errors are final.
func retryable(err error) bool {
	var apiErr hcloud.Error
	if errors.As(err, &apiErr) {
		switch apiErr.Code { //nolint:exhaustive // every other code is final
		case hcloud.ErrorCodeConflict,
			hcloud.ErrorCodeLocked,
			hcloud.ErrorCodeUniquenessError,
			hcloud.ErrorCodeRateLimitExceeded,
			hcloud.ErrorCodeTimeout,
			hcloud.ErrorCodeServerError,
			hcloud.ErrorCodeServiceError:
			return true
		}
		resp := apiErr.Response()
		return resp != nil && resp.StatusCode >= http.StatusInternalServerError
	}
	// A status code without an API error body comes from a proxy or load
	// balancer in front of the API, not from the API itself.
	if errors.Is(err, hcloud.ErrStatusCode) {
		return true
	}
	// Connection refused or reset, a failed DNS lookup, a lost response
	var netErr net.Error
	return errors.As(err, &netErr)
}

// withRetry runs op until it succeeds, fails with an error that is not
// retryable, or runs out of attempts or of the retry budget. set and unset are
// idempotent, so a rerun after a lost response finds the record in place, and
// a rerun after "already exists" adds the value to the RRSet another run
// created in the meantime.
func (a app) withRetry(ctx context.Context, op func(context.Context) error) error {
	retryCtx, cancel := context.WithTimeout(ctx, a.retryBudget)
	defer cancel()

	// giveUp returns err once the budget is used up. When the whole command
	// timed out instead, run reports that.
	giveUp := func(err error, attempts int) error {
		if ctx.Err() != nil {
			return err
		}
		return fmt.Errorf("%w (gave up after %s, attempts: %d)", err, a.retryBudget, attempts)
	}

	delay := a.retryDelay
	for attempt := 1; ; attempt++ {
		err := op(retryCtx)
		switch {
		case err == nil:
			return nil
		case retryCtx.Err() != nil:
			return giveUp(err, attempt)
		case !retryable(err):
			return err
		case attempt == a.attempts:
			return fmt.Errorf("%w (after %d attempts)", err, attempt)
		}

		_, _ = fmt.Fprintf(a.stderr, "Attempt %d of %d failed, retrying in %s: %v\n", attempt, a.attempts, delay, err)
		select {
		case <-retryCtx.Done():
			return giveUp(err, attempt)
		case <-time.After(delay):
		}
		delay *= 2
	}
}

func newClient(token string) *hcloud.Client {
	return hcloud.NewClient(clientOptions(token)...)
}

// connect reads the token, builds the API client and returns the first of the
// candidates that is a zone in the Hetzner account, so the most specific zone
// wins: with example.com and dev.example.com in the account, a name under
// dev.example.com goes to dev.example.com.
func (a app) connect(ctx context.Context, candidates []string) (*hcloud.Client, *hcloud.Zone, error) {
	token, err := readToken(a.tokenPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get token: %w", err)
	}
	client := a.newClient(token)

	for _, name := range candidates {
		zone, _, err := client.Zone.GetByName(ctx, name)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get zone %s: %w", name, err)
		}
		if zone != nil {
			return client, zone, nil
		}
	}
	return nil, nil, fmt.Errorf("no zone in the Hetzner account matches (tried %s)", strings.Join(candidates, ", "))
}

// deleteRRSet deletes rrset and waits for the deletion to finish, so the
// name is free for a new RRSet right after it returns.
func deleteRRSet(ctx context.Context, client *hcloud.Client, rrset *hcloud.ZoneRRSet) error {
	result, _, err := client.Zone.DeleteRRSet(ctx, rrset)
	if err != nil {
		return err
	}
	return client.Action.WaitFor(ctx, result.Action)
}

func deleteRRSetIfExists(ctx context.Context, client *hcloud.Client, zone *hcloud.Zone, name string) error {
	rrset, _, err := client.Zone.GetRRSetByNameAndType(ctx, zone, name, hcloud.ZoneRRSetTypeTXT)
	if err != nil || rrset == nil {
		return err
	}
	return deleteRRSet(ctx, client, rrset)
}

// cmdSet adds the challenge value and returns once Hetzner has applied it, so
// TrueNAS does not ask for validation too early. Setting a value that is
// already present is a no-op. It returns the authoritative nameservers of the
// zone the record went into, on every successful path including "already
// present" after a retry, for the nameserver check that run starts after the
// retries.
func (a app) cmdSet(ctx context.Context, args []string) ([]string, error) {
	fqdn, candidates, value, err := recordArgs(args)
	if err != nil {
		return nil, err
	}

	client, zone, err := a.connect(ctx, candidates)
	if err != nil {
		return nil, err
	}
	name := rrsetName(fqdn, zone)
	a.printf("Zone %s, record %s\n", zone.Name, name)

	if err := a.setRecord(ctx, client, zone, name, value); err != nil {
		return nil, err
	}
	return zone.AuthoritativeNameservers.Assigned, nil
}

// setRecord adds value to the TXT RRSet name and waits for the Hetzner action.
func (a app) setRecord(ctx context.Context, client *hcloud.Client, zone *hcloud.Zone, name, value string) error {
	records := []hcloud.ZoneRRSetRecord{{Value: quoteTXT(value)}}

	rrset, _, err := client.Zone.GetRRSetByNameAndType(ctx, zone, name, hcloud.ZoneRRSetTypeTXT)
	if err != nil {
		return fmt.Errorf("failed to get RRSet: %w", err)
	}

	if rrset == nil {
		result, _, err := client.Zone.CreateRRSet(ctx, zone, hcloud.ZoneRRSetCreateOpts{
			Name:    name,
			Type:    hcloud.ZoneRRSetTypeTXT,
			TTL:     new(txtTTL),
			Records: records,
		})
		if err != nil {
			return fmt.Errorf("failed to create RRSet: %w", err)
		}
		if err := client.Action.WaitFor(ctx, result.Action); err != nil {
			return fmt.Errorf("failed to create RRSet: %w", err)
		}
		return nil
	}

	if _, ok := findRecord(rrset, value); ok {
		a.printf("Record already present, nothing to add\n")
		return nil
	}

	action, _, err := client.Zone.AddRRSetRecords(ctx, rrset, hcloud.ZoneRRSetAddRecordsOpts{Records: records})
	if err != nil {
		return fmt.Errorf("failed to add record to RRSet: %w", err)
	}
	if err := client.Action.WaitFor(ctx, action); err != nil {
		return fmt.Errorf("failed to add record to RRSet: %w", err)
	}
	return nil
}

// cmdUnset removes the challenge value and waits for Hetzner to apply it.
// A value or RRSet that is already gone counts as success, so a repeated
// cleanup does not fail the TrueNAS job.
func (a app) cmdUnset(ctx context.Context, args []string) error {
	fqdn, candidates, value, err := recordArgs(args)
	if err != nil {
		return err
	}

	client, zone, err := a.connect(ctx, candidates)
	if err != nil {
		return err
	}
	name := rrsetName(fqdn, zone)
	a.printf("Zone %s, record %s\n", zone.Name, name)

	rrset, _, err := client.Zone.GetRRSetByNameAndType(ctx, zone, name, hcloud.ZoneRRSetTypeTXT)
	if err != nil {
		return fmt.Errorf("failed to get RRSet: %w", err)
	}
	if rrset == nil {
		a.printf("Record not found, nothing to remove\n")
		return nil
	}
	record, ok := findRecord(rrset, value)
	if !ok {
		a.printf("Record not found, nothing to remove (stored values: %s)\n", strings.Join(recordValues(rrset), ", "))
		return nil
	}

	// Delete the RRSet instead of removing its last record. Whether the API
	// accepts or keeps an empty RRSet is not verified; deleting has the same
	// effect either way.
	if len(rrset.Records) == 1 {
		if err := deleteRRSet(ctx, client, rrset); err != nil {
			return fmt.Errorf("failed to delete RRSet: %w", err)
		}
		return nil
	}

	action, _, err := client.Zone.RemoveRRSetRecords(ctx, rrset, hcloud.ZoneRRSetRemoveRecordsOpts{
		// The stored form, so the API matches it even if it differs from quoteTXT
		Records: []hcloud.ZoneRRSetRecord{record},
	})
	if err != nil {
		return fmt.Errorf("failed to remove record: %w", err)
	}
	if err := client.Action.WaitFor(ctx, action); err != nil {
		return fmt.Errorf("failed to remove record: %w", err)
	}
	return nil
}

func (a app) cmdInit() error {
	path := a.tokenPath

	a.printf("Creating %s...\n", path)

	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0600)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to close %s: %w", path, err)
	}

	a.printf("Change mode of %s to 0600...\n", path)
	if err := os.Chmod(path, 0600); err != nil {
		return fmt.Errorf("failed to chmod %s: %w", path, err)
	}
	return nil
}

func (a app) cmdTest(ctx context.Context, args []string) (err error) {
	candidates, err := domainArg(args)
	if err != nil {
		return err
	}

	warnings, err := checkTokenFile(a.tokenPath)
	if err != nil {
		return fmt.Errorf("failed to test token: %w", err)
	}
	for _, w := range warnings {
		a.printf("%s\n", w)
	}

	a.printf("Looking up zone for: %s\n", candidates[0])
	client, zone, err := a.connect(ctx, candidates)
	if err != nil {
		return err
	}
	a.printf("Found zone: %s\n", zone.Name)

	// A run that died between create and delete leaves the test RRSet behind,
	// and CreateRRSet would then fail on every later run.
	if err := deleteRRSetIfExists(ctx, client, zone, testRecordName); err != nil {
		return fmt.Errorf("failed to remove leftover test record: %w", err)
	}

	quotedValue := quoteTXT(testRecordText)
	a.printf("Creating TXT record: %s = %s\n", testRecordName, quotedValue)

	result, _, err := client.Zone.CreateRRSet(ctx, zone, hcloud.ZoneRRSetCreateOpts{
		Name:    testRecordName,
		Type:    hcloud.ZoneRRSetTypeTXT,
		TTL:     new(txtTTL),
		Records: []hcloud.ZoneRRSetRecord{{Value: quotedValue}},
	})
	if err != nil {
		return fmt.Errorf("failed to create test record: %w", err)
	}

	// Best-effort cleanup if anything below fails. It gets its own deadline,
	// because a timeout has already used up ctx.
	defer func() {
		if err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
			defer cancel()
			_ = deleteRRSetIfExists(cleanupCtx, client, zone, testRecordName)
		}
	}()

	// The same wait set does, so test fails where set would
	if err = client.Action.WaitFor(ctx, result.Action); err != nil {
		return fmt.Errorf("failed to create test record: %w", err)
	}

	a.printf("Deleting test record...\n")

	rrset, _, err := client.Zone.GetRRSetByNameAndType(ctx, zone, testRecordName, hcloud.ZoneRRSetTypeTXT)
	if err != nil {
		return fmt.Errorf("failed to get test RRSet: %w", err)
	}
	if rrset == nil {
		return errors.New("test record not found after creating it")
	}

	if err = deleteRRSet(ctx, client, rrset); err != nil {
		return fmt.Errorf("failed to delete test record: %w", err)
	}

	a.printf("Test passed!\n")
	return nil
}

// run executes the command in args (args[0] is the program name) and returns the exit code.
func run(args []string, a app) int {
	prog := "tah"
	if len(args) > 0 {
		prog = args[0]
	}

	ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
	defer cancel()

	var err error
	if len(args) < 2 {
		err = usageError{"Command is missing!"}
	} else {
		switch args[1] {
		case "set":
			var servers []string
			err = a.withRetry(ctx, func(ctx context.Context) error {
				var setErr error
				servers, setErr = a.cmdSet(ctx, args)
				return setErr
			})
			// Outside withRetry, so the check gets the rest of the command
			// budget (at most apiTimeout-retryBudget), not what is left of
			// the retry budget. It only warns, so it never triggers a retry.
			if err == nil {
				a.waitForNameservers(ctx, servers, args[3], args[4])
			}
		case "unset":
			err = a.withRetry(ctx, func(ctx context.Context) error { return a.cmdUnset(ctx, args) })
		case "init":
			err = a.cmdInit()
		case "test":
			err = a.cmdTest(ctx, args)
		case "version", "--version":
			a.printf("%s\n", Version)
		case "help":
			a.printf(helpText, prog)
		default:
			err = usageError{fmt.Sprintf("Invalid command: %s!", args[1])}
		}
	}

	if err != nil {
		// Checked on the context, not on err: how a cancelled request is
		// wrapped depends on hcloud-go and net/http.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("%w (timed out after %s)", err, a.timeout)
		}
		_, _ = fmt.Fprintln(a.stderr, err)
		var ue usageError
		if errors.As(err, &ue) {
			a.printf("Use \"%s help\" for help!\n", prog)
		}
		return 1
	}
	return 0
}

// defaultApp is the app the binary runs with: the real terminal, the token
// file in $HOME, the Hetzner API and the production timeouts.
func defaultApp() app {
	return app{
		stdout:      os.Stdout,
		stderr:      os.Stderr,
		tokenPath:   os.ExpandEnv("$HOME/.tahtoken"),
		newClient:   newClient,
		timeout:     apiTimeout,
		retryBudget: retryBudget,
		attempts:    retryAttempts,
		retryDelay:  retryDelay,
		ns:          defaultNSCheck(),
	}
}

func main() {
	os.Exit(run(os.Args, defaultApp()))
}
