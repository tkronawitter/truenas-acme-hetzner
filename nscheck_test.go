package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const challengeName = "_acme-challenge.example.com."

// fakeDNS is an authoritative nameserver on a local UDP port. It answers every
// TXT query the same way: NXDOMAIN (or the stale values) until the query
// numbered appearAt, the challenge value from then on. The real resolver code
// of the binary talks to it.
type fakeDNS struct {
	conn net.PacketConn

	// Set by newFakeDNS before the server goroutine starts, read under mu;
	// tests never write them afterwards (the UDP round trip is no
	// synchronisation the race detector sees).
	mu       sync.Mutex
	value    string   // served from query appearAt on; "" = never
	stale    []string // served before appearAt; nil = NXDOMAIN
	appearAt int      // 1-based query number
	queries  int
}

// newFakeDNS starts a fakeDNS that serves value from query appearAt on, and
// stale (NOERROR with old values) or, without stale, NXDOMAIN before that.
func newFakeDNS(t *testing.T, value string, appearAt int, stale ...string) *fakeDNS {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDNS{conn: conn, value: value, appearAt: appearAt, stale: stale}
	t.Cleanup(func() { _ = conn.Close() })
	go d.serve()
	return d
}

func (d *fakeDNS) addr() string { return d.conn.LocalAddr().String() }

func (d *fakeDNS) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.queries
}

func (d *fakeDNS) serve() {
	buf := make([]byte, 1500)
	for {
		n, from, err := d.conn.ReadFrom(buf)
		if err != nil {
			return
		}
		if resp := d.answer(buf[:n]); resp != nil {
			_, _ = d.conn.WriteTo(resp, from)
		}
	}
}

func (d *fakeDNS) answer(req []byte) []byte {
	var p dnsmessage.Parser
	h, err := p.Start(req)
	if err != nil {
		return nil
	}
	q, err := p.Question()
	if err != nil {
		return nil
	}

	var values []string
	if q.Type == dnsmessage.TypeTXT {
		d.mu.Lock()
		d.queries++
		if d.value != "" && d.queries >= d.appearAt {
			values = []string{d.value}
		} else {
			values = d.stale
		}
		d.mu.Unlock()
	}

	rcode := dnsmessage.RCodeSuccess
	if values == nil {
		rcode = dnsmessage.RCodeNameError
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true, RCode: rcode})
	if b.StartQuestions() != nil || b.Question(q) != nil || b.StartAnswers() != nil {
		return nil
	}
	for _, v := range values {
		rh := dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeTXT, Class: dnsmessage.ClassINET, TTL: 60}
		if b.TXTResource(rh, dnsmessage.TXTResource{TXT: []string{v}}) != nil {
			return nil
		}
	}
	out, err := b.Finish()
	if err != nil {
		return nil
	}
	return out
}

// closedUDPAddr returns a local UDP address nothing listens on.
func closedUDPAddr(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := conn.LocalAddr().String()
	_ = conn.Close()
	return addr
}

// runSet runs set for the challenge name with an app from fakeHetzner.app,
// after the test adjusted it.
func runSet(a app) (int, string, string) {
	code := run([]string{"tah", "set", "example.com", "_acme-challenge.example.com", "token-value"}, a)
	return code, a.stdout.(*bytes.Buffer).String(), a.stderr.(*bytes.Buffer).String()
}

func TestSetWaitsUntilAllNameserversServeTheValue(t *testing.T) {
	f := newFakeHetzner(t)
	fast := newFakeDNS(t, "token-value", 1)
	slow := newFakeDNS(t, "token-value", 4) // NXDOMAIN for three polls
	f.setNameservers(fast.addr(), slow.addr())
	a, _, _ := f.app()

	code, stdout, stderr := runSet(a)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "Challenge served by 2 of 2 nameservers") {
		t.Errorf("stdout = %q, want the served-by-all note", stdout)
	}
	if got := slow.count(); got != 4 {
		t.Errorf("slow nameserver got %d queries, want 4 (polled until the value appears, then no more)", got)
	}
	if got := fast.count(); got != 1 {
		t.Errorf("fast nameserver got %d queries, want 1 (not polled again once it serves the value)", got)
	}
	assertAllWaited(t, f)
}

func TestSetWaitsWhileANameserverServesAStaleValue(t *testing.T) {
	f := newFakeHetzner(t)
	ns := newFakeDNS(t, "token-value", 3, "old-token") // NOERROR with the previous challenge, not NXDOMAIN
	f.setNameservers(ns.addr())
	a, _, _ := f.app()

	code, stdout, stderr := runSet(a)
	if code != 0 || !strings.Contains(stdout, "Challenge served by 1 of 1 nameservers") {
		t.Fatalf("exit code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if got := ns.count(); got != 3 {
		t.Errorf("nameserver got %d queries, want 3", got)
	}
}

func TestSetNameserverCheckDeadlineWarnsAndSucceeds(t *testing.T) {
	f := newFakeHetzner(t)
	ok := newFakeDNS(t, "token-value", 1)
	never := newFakeDNS(t, "", 0)
	f.setNameservers(ok.addr(), never.addr())
	a, _, _ := f.app()
	a.ns.maxWait = 300 * time.Millisecond

	start := time.Now()
	code, stdout, stderr := runSet(a)
	elapsed := time.Since(start)

	if code != 0 || stderr != "" {
		t.Fatalf("exit code = %d, stderr = %q, want 0 and no error", code, stderr)
	}
	if !strings.Contains(stdout, "Warning: after") || !strings.Contains(stdout, "still missing on "+never.addr()) {
		t.Errorf("stdout = %q, want a warning naming %s", stdout, never.addr())
	}
	if strings.Contains(stdout, ok.addr()) {
		t.Errorf("stdout = %q, the nameserver that serves the value must not be listed", stdout)
	}
	if elapsed > 2*time.Second {
		t.Errorf("set took %s, want it to stop at the 300ms check cap", elapsed)
	}
	if never.count() < 2 {
		t.Errorf("missing nameserver got %d queries, want it polled repeatedly", never.count())
	}
}

func TestSetSomeNameserversUnreachable(t *testing.T) {
	f := newFakeHetzner(t)
	ok := newFakeDNS(t, "token-value", 1)
	closed := closedUDPAddr(t)
	f.setNameservers(ok.addr(), closed)
	a, _, _ := f.app()

	code, stdout, stderr := runSet(a)
	if code != 0 || stderr != "" {
		t.Fatalf("exit code = %d, stderr = %q, want 0 and no error", code, stderr)
	}
	if !strings.Contains(stdout, "Warning: cannot query "+closed) {
		t.Errorf("stdout = %q, want a warning for %s", stdout, closed)
	}
	if !strings.Contains(stdout, "Challenge served by 1 of 2 nameservers") {
		t.Errorf("stdout = %q, want the check to finish on the reachable nameserver", stdout)
	}
}

func TestSetAllNameserversUnreachableSkipsCheck(t *testing.T) {
	f := newFakeHetzner(t)
	f.setNameservers("hydrogen.ns.hetzner.com", "oxygen.ns.hetzner.com")
	a, _, _ := f.app()
	var mu sync.Mutex
	calls := 0
	a.ns.lookup = func(ctx context.Context, server, name string) ([]string, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return nil, &net.OpError{Op: "read", Net: "udp", Err: errors.New("i/o timeout")}
	}

	code, stdout, stderr := runSet(a)
	if code != 0 || stderr != "" {
		t.Fatalf("exit code = %d, stderr = %q, want 0 and no error", code, stderr)
	}
	if strings.Count(stdout, "Warning") != 1 || !strings.Contains(stdout, "nameserver check skipped") || !strings.Contains(stdout, "port 53") {
		t.Errorf("stdout = %q, want exactly one warning about port 53", stdout)
	}
	if calls != 2 {
		t.Errorf("lookup called %d times, want 2 (one query per nameserver, then give up)", calls)
	}
}

func TestSetNameserverHostnameDoesNotResolve(t *testing.T) {
	// A DNSError for the nameserver's own name is not "challenge not served yet".
	f := newFakeHetzner(t)
	f.setNameservers("ns.invalid")
	a, _, _ := f.app()
	a.ns.lookup = func(ctx context.Context, server, name string) ([]string, error) {
		return nil, &net.DNSError{Err: "no such host", Name: server, IsNotFound: true}
	}

	code, stdout, _ := runSet(a)
	if code != 0 || !strings.Contains(stdout, "nameserver check skipped, cannot query ns.invalid") {
		t.Errorf("exit code = %d, stdout = %q, want the skip warning", code, stdout)
	}
}

func TestSetSkipsCheckNearTheTimeout(t *testing.T) {
	f := newFakeHetzner(t)
	f.setNameservers("hydrogen.ns.hetzner.com")
	a, _, _ := f.app()
	a.ns.lookup = func(ctx context.Context, server, name string) ([]string, error) {
		t.Error("lookup called although too little time was left")
		return nil, nil
	}
	a.ns.margin = time.Second
	a.ns.minWait = time.Second
	a.timeout = 1500 * time.Millisecond // leaves < margin + minWait after the API phase

	code, stdout, stderr := runSet(a)
	if code != 0 || stderr != "" {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "Nameserver check skipped: not enough time left") {
		t.Errorf("stdout = %q, want the skip note", stdout)
	}
}

func TestSetCheckStaysInsideTheCommandTimeout(t *testing.T) {
	// The cap is larger than the whole budget: the deadline minus the margin wins.
	f := newFakeHetzner(t)
	never := newFakeDNS(t, "", 0)
	f.setNameservers(never.addr())
	a, _, _ := f.app()
	a.timeout = 800 * time.Millisecond
	a.ns.maxWait = 15 * time.Second
	a.ns.margin = 200 * time.Millisecond

	start := time.Now()
	code, stdout, stderr := runSet(a)
	elapsed := time.Since(start)

	if code != 0 || stderr != "" {
		t.Fatalf("exit code = %d, stderr = %q, want 0 and no timeout error", code, stderr)
	}
	if elapsed >= a.timeout {
		t.Errorf("set took %s, want it to finish before the %s timeout", elapsed, a.timeout)
	}
	if !strings.Contains(stdout, "still missing on "+never.addr()) {
		t.Errorf("stdout = %q, want the deadline warning", stdout)
	}
}

func TestSetWithoutListedNameservers(t *testing.T) {
	code, stdout, stderr := runCmd(newFakeHetzner(t), "set", "example.com", "_acme-challenge.example.com", "token-value")
	if code != 0 || !strings.Contains(stdout, "zone lists no authoritative nameservers") {
		t.Errorf("exit code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
}

func TestSetAlreadyPresentStillChecks(t *testing.T) {
	f := newFakeHetzner(t)
	f.addRecords("_acme-challenge", `"token-value"`)
	ns := newFakeDNS(t, "token-value", 1)
	f.setNameservers(ns.addr())
	a, _, _ := f.app()

	code, stdout, _ := runSet(a)
	if code != 0 || !strings.Contains(stdout, "Record already present") || !strings.Contains(stdout, "Challenge served by 1 of 1") {
		t.Errorf("exit code = %d, stdout = %q", code, stdout)
	}
}

func TestUnsetDoesNotCheckNameservers(t *testing.T) {
	f := newFakeHetzner(t)
	f.addRecords("_acme-challenge", `"token-value"`)
	ns := newFakeDNS(t, "token-value", 1)
	f.setNameservers(ns.addr())

	code, _, stderr := runCmd(f, "unset", "example.com", "_acme-challenge.example.com", "token-value")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if ns.count() != 0 {
		t.Errorf("unset sent %d DNS queries, want none", ns.count())
	}
}

func TestQueryErrorHidesResolverAddress(t *testing.T) {
	err := &net.DNSError{Err: "server misbehaving", Name: challengeName, Server: "192.0.2.1:53"}
	if got := queryError(err); got != "server misbehaving" {
		t.Errorf("queryError = %q, want the reason without the resolver address", got)
	}
	if got := queryError(errors.New("plain")); got != "plain" {
		t.Errorf("queryError = %q, want plain", got)
	}
}

func TestNotServedYet(t *testing.T) {
	name := "_acme-challenge.example.com"
	tests := []struct {
		err  error
		want bool
	}{
		{&net.DNSError{Name: name, IsNotFound: true}, true},
		{&net.DNSError{Name: name + ".", IsNotFound: true}, true},
		{&net.DNSError{Name: "hydrogen.ns.hetzner.com", IsNotFound: true}, false},
		{&net.DNSError{Name: name, IsTimeout: true}, false},
		{&net.OpError{Op: "read", Err: errors.New("connection refused")}, false},
	}
	for _, tt := range tests {
		if got := notServedYet(tt.err, name); got != tt.want {
			t.Errorf("notServedYet(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}

// A rerun after a lost create response finds the value in place and does not
// wait for the first attempt's action; the nameserver check still runs.
func TestSetChecksNameserversAfterARetry(t *testing.T) {
	f := newFakeHetzner(t)
	f.failWith("POST /zones/42/rrsets", failure{n: 1, drop: dropAfter})
	ns := newFakeDNS(t, "token-value", 2)
	f.setNameservers(ns.addr())
	a, _, _ := f.app()

	code, stdout, stderr := runSet(a)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "Attempt 1 of 3 failed") {
		t.Errorf("stderr = %q, want the failed first attempt", stderr)
	}
	if !strings.Contains(stdout, "Record already present") || !strings.Contains(stdout, "Challenge served by 1 of 1") {
		t.Errorf("stdout = %q, want the rerun to find the value and the check to run", stdout)
	}
}

// The check runs on the command's context, after the retries: its wait is not
// cut short by the retry budget.
func TestSetCheckIsNotLimitedByTheRetryBudget(t *testing.T) {
	f := newFakeHetzner(t)
	never := newFakeDNS(t, "", 0)
	f.setNameservers(never.addr())
	a, _, _ := f.app()
	a.retryBudget = 200 * time.Millisecond
	a.timeout = 5 * time.Second
	a.ns.maxWait = 800 * time.Millisecond

	start := time.Now()
	code, stdout, stderr := runSet(a)
	elapsed := time.Since(start)

	if code != 0 || stderr != "" {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if elapsed < 700*time.Millisecond {
		t.Errorf("set took %s, want the check to use its own 800ms, not the 200ms retry budget", elapsed)
	}
	if !strings.Contains(stdout, "still missing on "+never.addr()) {
		t.Errorf("stdout = %q, want the deadline warning", stdout)
	}
}

func TestSetFailureSkipsNameserverCheck(t *testing.T) {
	f := newFakeHetzner(t)
	f.failOn("POST /zones/42/rrsets", 1) // 422, not retryable
	ns := newFakeDNS(t, "token-value", 1)
	f.setNameservers(ns.addr())

	code, _, _ := runCmd(f, "set", "example.com", "_acme-challenge.example.com", "token-value")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if ns.count() != 0 {
		t.Errorf("failed set sent %d DNS queries, want none", ns.count())
	}
}

// A record in a subzone is checked on the subzone's nameservers, the ones of
// the zone connect found, not on the parent zone's.
func TestSetChecksTheSubzoneNameservers(t *testing.T) {
	f := newFakeHetzner(t)
	f.addZone("dev.example.com")
	parent := newFakeDNS(t, "", 0)
	sub := newFakeDNS(t, "token-value", 2)
	f.setNameservers(parent.addr())
	f.setZoneNameservers("dev.example.com", sub.addr())
	a, stdout, stderr := f.app()

	code := run([]string{"tah", "set", "nas.dev.example.com", "_acme-challenge.nas.dev.example.com", "token-value"}, a)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Zone dev.example.com") || !strings.Contains(stdout.String(), "Challenge served by 1 of 1") {
		t.Errorf("stdout = %q, want the subzone and the check on its nameserver", stdout.String())
	}
	if parent.count() != 0 {
		t.Errorf("parent zone nameserver got %d queries, want none", parent.count())
	}
	if sub.count() != 2 {
		t.Errorf("subzone nameserver got %d queries, want 2", sub.count())
	}
}

// A nameserver without a port is asked on port 53. The context is already
// cancelled, so nothing goes out on the network.
func TestLookupTXTDirectDefaultPort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := lookupTXTDirect(ctx, "192.0.2.1", challengeName); err == nil {
		t.Error("lookup with a cancelled context succeeded")
	}
}

// The production split of the 50s budget: retries first, the check gets the rest.
func TestDefaultNSCheck(t *testing.T) {
	c := defaultApp().ns
	if c.lookup == nil {
		t.Fatal("lookup is nil")
	}
	if c.maxWait != 10*time.Second || retryBudget+c.maxWait > apiTimeout {
		t.Errorf("maxWait %s with retryBudget %s, want 10s and together at most apiTimeout %s", c.maxWait, retryBudget, apiTimeout)
	}
	if c.minWait <= 0 || c.margin <= 0 || c.interval <= 0 || c.queryTimeout <= 0 || c.queryTimeout > c.maxWait {
		t.Errorf("nsCheck %+v: want positive limits and a query timeout below maxWait", c)
	}
}
