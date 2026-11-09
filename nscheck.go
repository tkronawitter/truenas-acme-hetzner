package main

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
	"time"
)

// txtLookup asks one nameserver directly for the TXT values of name.
type txtLookup func(ctx context.Context, server, name string) ([]string, error)

// nsCheck configures the wait in set until every authoritative nameserver
// serves the challenge value. The check never fails a command: when it runs
// out of time or cannot reach a nameserver, it warns and set still exits 0,
// so TrueNAS's own propagation delay and the ACME validation decide as before.
type nsCheck struct {
	lookup txtLookup
	// maxWait caps the check, even when the command has more time left.
	maxWait time.Duration
	// margin is kept free before the command deadline for output and exit.
	margin time.Duration
	// minWait: with less time left than this, the check is skipped.
	minWait time.Duration
	// interval between two polls of the nameservers that still miss the value.
	interval time.Duration
	// queryTimeout bounds one query to one nameserver.
	queryTimeout time.Duration
}

func defaultNSCheck() nsCheck {
	return nsCheck{
		lookup: lookupTXTDirect,
		// The 50s apiTimeout is split: 40s for set with its retries, the
		// rest (10s) for this check.
		maxWait: apiTimeout - retryBudget,
		// tah's own 50s already stays 10s below the TrueNAS Script Timeout.
		margin:       time.Second,
		minWait:      2 * time.Second,
		interval:     time.Second,
		queryTimeout: 2 * time.Second,
	}
}

// lookupTXTDirect sends the TXT query straight to server, without the system's
// recursive resolver and its cache. server is a hostname or IP, port 53 unless
// it carries one (tests use a local port).
func lookupTXTDirect(ctx context.Context, server, name string) ([]string, error) {
	addr := server
	if _, _, err := net.SplitHostPort(server); err != nil {
		addr = net.JoinHostPort(server, "53")
	}
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	return r.LookupTXT(ctx, name)
}

// fqdn returns name with exactly one trailing dot.
func fqdn(name string) string {
	return strings.TrimSuffix(name, ".") + "."
}

// notServedYet reports whether err means the nameserver answered but does not
// have the name (yet). Other errors (timeout, refused, the nameserver's own
// hostname not resolving) mean the nameserver could not be asked.
func notServedYet(err error, name string) bool {
	var de *net.DNSError
	return errors.As(err, &de) && de.IsNotFound && fqdn(de.Name) == fqdn(name)
}

// queryError describes err without the "on <address>" part of a DNSError:
// with the custom Dial, that address is the system resolver from
// resolv.conf, not the nameserver that was actually asked.
func queryError(err error) string {
	var de *net.DNSError
	if errors.As(err, &de) {
		return de.Err
	}
	return err.Error()
}

type nsResult struct {
	found bool
	err   error
}

// queryAll asks every server in parallel, so one slow server does not add up.
func (c nsCheck) queryAll(ctx context.Context, servers []string, name, value string) []nsResult {
	results := make([]nsResult, len(servers))
	var wg sync.WaitGroup
	for i, s := range servers {
		wg.Go(func() {
			qctx, cancel := context.WithTimeout(ctx, c.queryTimeout)
			defer cancel()
			values, err := c.lookup(qctx, s, name)
			results[i] = nsResult{found: err == nil && slices.Contains(values, value), err: err}
		})
	}
	wg.Wait()
	return results
}

// waitForNameservers polls servers until each one serves value for name, the
// check deadline passes, or no server can be reached. It only prints; it never
// returns an error.
func (a app) waitForNameservers(ctx context.Context, servers []string, name, value string) {
	c := a.ns
	name = fqdn(name)
	if len(servers) == 0 {
		a.printf("Nameserver check skipped: the zone lists no authoritative nameservers\n")
		return
	}

	wait := c.maxWait
	if deadline, ok := ctx.Deadline(); ok {
		wait = min(wait, time.Until(deadline)-c.margin)
	}
	if wait < c.minWait {
		a.printf("Nameserver check skipped: not enough time left before the %s timeout\n", a.timeout)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	start := time.Now()
	pending := slices.Clone(servers)
	var unreachable []string
	for round := 1; ; round++ {
		results := c.queryAll(ctx, pending, name, value)
		var missing []string
		for i, s := range pending {
			r := results[i]
			switch {
			case r.found:
			case round == 1 && r.err != nil && ctx.Err() == nil && !notServedYet(r.err, name):
				unreachable = append(unreachable, s)
			default:
				missing = append(missing, s)
			}
		}

		if round == 1 && len(unreachable) > 0 {
			if len(unreachable) == len(servers) {
				a.printf("Warning: nameserver check skipped, cannot query %s (is outbound port 53 blocked?): %s\n",
					strings.Join(unreachable, ", "), queryError(results[0].err))
				return
			}
			a.printf("Warning: cannot query %s, checking the other nameservers only\n", strings.Join(unreachable, ", "))
		}

		if len(missing) == 0 {
			a.printf("Challenge served by %d of %d nameservers after %s\n",
				len(servers)-len(unreachable), len(servers), time.Since(start).Round(100*time.Millisecond))
			return
		}
		pending = missing

		select {
		case <-ctx.Done():
			a.printf("Warning: after %s the challenge is still missing on %s; continuing, the TrueNAS propagation delay and the ACME validation decide\n",
				time.Since(start).Round(100*time.Millisecond), strings.Join(missing, ", "))
			return
		case <-time.After(c.interval):
		}
	}
}
