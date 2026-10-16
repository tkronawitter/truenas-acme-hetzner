package main

import (
	"context"
	"errors"
	"fmt"
	"io"
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
// swap the output streams, the token file, the Hetzner API endpoint and the timeout.
type app struct {
	stdout    io.Writer
	stderr    io.Writer
	tokenPath string
	newClient func(token string) *hcloud.Client
	timeout   time.Duration
}

// usageError marks invalid command line input; run prints the help hint after it.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

// printf writes to stdout. A failed write to the terminal is not actionable.
func (a app) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(a.stdout, format, args...)
}

// getDomain extracts registrable domain (e.g., "example.com" from "sub.example.com")
func getDomain(fqdn string) string {
	domain, _ := publicsuffix.EffectiveTLDPlusOne(strings.TrimSuffix(fqdn, "."))
	return domain
}

// getSub extracts subdomain part (e.g., "_acme-challenge.sub" from "_acme-challenge.sub.example.com")
func getSub(fqdn string) string {
	domain := getDomain(fqdn)
	fqdn = strings.TrimSuffix(fqdn, ".")
	if fqdn == domain {
		return ""
	}
	return strings.TrimSuffix(fqdn, "."+domain)
}

func domainArg(args []string) (string, error) {
	if len(args) < 3 {
		return "", usageError{"domain is missing!"}
	}
	domain := getDomain(args[2])
	if domain == "" {
		return "", usageError{fmt.Sprintf("Invalid domain: %s!", args[2])}
	}
	return domain, nil
}

// recordArgs returns the zone, the RRSet name inside it and the TXT value
// for set and unset: <domain> <validation_name> <validation_context>.
func recordArgs(args []string) (domain, name, value string, err error) {
	domain, err = domainArg(args)
	if err != nil {
		return "", "", "", err
	}
	if len(args) < 4 {
		return "", "", "", usageError{"validation_name is missing!"}
	}
	if len(args) < 5 {
		return "", "", "", usageError{"validation_context is missing!"}
	}
	return domain, getSub(args[3]), args[4], nil
}

// quoteTXT quotes a TXT value as the Hetzner API expects it.
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

func readToken(path string) (string, error) {
	out, err := os.ReadFile(path)
	return strings.Trim(string(out), "\n"), err
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

func newClient(token string) *hcloud.Client {
	return hcloud.NewClient(hcloud.WithToken(token))
}

// connect reads the token, builds the API client and looks up the zone.
func (a app) connect(ctx context.Context, domain string) (*hcloud.Client, *hcloud.Zone, error) {
	token, err := readToken(a.tokenPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get token: %w", err)
	}
	client := a.newClient(token)

	zone, _, err := client.Zone.GetByName(ctx, domain)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get zone: %w", err)
	}
	if zone == nil {
		return nil, nil, fmt.Errorf("zone '%s' not found in Hetzner account", domain)
	}
	return client, zone, nil
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
// already present is a no-op.
func (a app) cmdSet(ctx context.Context, args []string) error {
	domain, name, value, err := recordArgs(args)
	if err != nil {
		return err
	}

	client, zone, err := a.connect(ctx, domain)
	if err != nil {
		return err
	}

	records := []hcloud.ZoneRRSetRecord{{Value: quoteTXT(value)}}

	rrset, _, err := client.Zone.GetRRSetByNameAndType(ctx, zone, name, hcloud.ZoneRRSetTypeTXT)
	if err != nil {
		return fmt.Errorf("failed to get RRSet: %w", err)
	}

	if rrset == nil {
		result, _, err := client.Zone.CreateRRSet(ctx, zone, hcloud.ZoneRRSetCreateOpts{
			Name:    name,
			Type:    hcloud.ZoneRRSetTypeTXT,
			TTL:     hcloud.Ptr(txtTTL),
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
	domain, name, value, err := recordArgs(args)
	if err != nil {
		return err
	}

	client, zone, err := a.connect(ctx, domain)
	if err != nil {
		return err
	}

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

	// Removing the last record might leave an empty RRSet, which the API may
	// not accept; deleting the RRSet has the same effect and is always valid.
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
	domain, err := domainArg(args)
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

	a.printf("Looking up zone: %s\n", domain)
	client, zone, err := a.connect(ctx, domain)
	if err != nil {
		return err
	}

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
		TTL:     hcloud.Ptr(txtTTL),
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
			err = a.cmdSet(ctx, args)
		case "unset":
			err = a.cmdUnset(ctx, args)
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

func main() {
	os.Exit(run(os.Args, app{
		stdout:    os.Stdout,
		stderr:    os.Stderr,
		tokenPath: os.ExpandEnv("$HOME/.tahtoken"),
		newClient: newClient,
		timeout:   apiTimeout,
	}))
}
