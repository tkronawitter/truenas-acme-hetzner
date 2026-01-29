package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"golang.org/x/net/publicsuffix"
	"golang.org/x/sys/unix"
)

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

func mustGetCommandArg() string {

	if len(os.Args) < 2 {

		fmt.Fprintf(os.Stderr, "Command is missing!\n")
		fmt.Printf("Use \"%s help\" for help!\n", os.Args[0])
		os.Exit(1)
	}

	if os.Args[1] != "set" && os.Args[1] != "unset" && os.Args[1] != "init" && os.Args[1] != "test" && os.Args[1] != "help" {
		fmt.Fprintf(os.Stderr, "Invalid command: %s!\n", os.Args[1])
		fmt.Printf("Use \"%s help\" for help!\n", os.Args[0])
		os.Exit(1)
	}

	return os.Args[1]
}

func mustGetDomainArg() string {

	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "domain is missing!\n")
		fmt.Printf("Use \"%s help\" for help!\n", os.Args[0])
		os.Exit(1)
	}

	return getDomain(os.Args[2])
}

func mustGetValidationNameArg() string {

	if len(os.Args) < 4 {
		fmt.Fprintf(os.Stderr, "validation_name is missing!\n")
		fmt.Printf("Use \"%s help\" for help!\n", os.Args[0])
		os.Exit(1)
	}

	return getSub(os.Args[3])
}

func mustGetValidationContextArg() string {

	if len(os.Args) < 5 {
		fmt.Fprintf(os.Stderr, "validation_context is missing!\n")
		fmt.Printf("Use \"%s help\" for help!\n", os.Args[0])
		os.Exit(1)
	}

	return os.Args[4]
}

func getToken() (string, error) {

	path := os.ExpandEnv("$HOME/.tahtoken")

	out, err := os.ReadFile(path)

	return strings.Trim(string(out), "\n"), err
}

func testTokenFile() error {

	path := os.ExpandEnv("$HOME/.tahtoken")

	statT := new(unix.Stat_t)

	err := unix.Stat(path, statT)
	if err != nil {
		return err
	}

	if statT.Uid != uint32(os.Geteuid()) {
		fmt.Printf("Different user for config file: %d/%d\n", statT.Uid, os.Geteuid())
	}

	if statT.Gid != uint32(os.Getegid()) {
		fmt.Printf("Different group for config file: %d/%d\n", statT.Gid, os.Getegid())
	}

	if statT.Mode != 0o100600 {
		fmt.Printf("Dangerous file mode: %o, use 0600!\n", statT.Mode)
	}

	return nil
}

func newClient(token string) *hcloud.Client {
	return hcloud.NewClient(hcloud.WithToken(token))
}

func Set() {

	domain := mustGetDomainArg()
	validationName := mustGetValidationNameArg()
	validationContext := mustGetValidationContextArg()

	token, err := getToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get token: %s\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	client := newClient(token)

	// Get zone by name
	zoneName := strings.TrimSuffix(domain, ".")
	zone, _, err := client.Zone.GetByName(ctx, zoneName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get zone: %s\n", err)
		os.Exit(1)
	}
	if zone == nil {
		fmt.Fprintf(os.Stderr, "Zone '%s' not found in Hetzner account\n", zoneName)
		os.Exit(1)
	}

	// Quote TXT value per Hetzner API requirements
	quotedValue := fmt.Sprintf(`"%s"`, validationContext)

	// Check if RRSet exists
	rrset, _, err := client.Zone.GetRRSetByNameAndType(ctx, zone, validationName, hcloud.ZoneRRSetTypeTXT)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get RRSet: %s\n", err)
		os.Exit(1)
	}

	ttl := 3600
	if rrset == nil {
		// Create new RRSet with record
		_, _, err := client.Zone.CreateRRSet(ctx, zone, hcloud.ZoneRRSetCreateOpts{
			Name:    validationName,
			Type:    hcloud.ZoneRRSetTypeTXT,
			TTL:     &ttl,
			Records: []hcloud.ZoneRRSetRecord{{Value: quotedValue}},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create RRSet: %s\n", err)
			os.Exit(1)
		}
	} else {
		// Add to existing RRSet
		_, _, err = client.Zone.AddRRSetRecords(ctx, rrset, hcloud.ZoneRRSetAddRecordsOpts{
			Records: []hcloud.ZoneRRSetRecord{{Value: quotedValue}},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to add record to RRSet: %s\n", err)
			os.Exit(1)
		}
	}
}

func Unset() {

	domain := mustGetDomainArg()
	validationName := mustGetValidationNameArg()
	validationContext := mustGetValidationContextArg()

	token, err := getToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get token: %s\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	client := newClient(token)

	zoneName := strings.TrimSuffix(domain, ".")
	zone, _, err := client.Zone.GetByName(ctx, zoneName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get zone: %s\n", err)
		os.Exit(1)
	}
	if zone == nil {
		fmt.Fprintf(os.Stderr, "Zone '%s' not found in Hetzner account\n", zoneName)
		os.Exit(1)
	}

	rrset, _, err := client.Zone.GetRRSetByNameAndType(ctx, zone, validationName, hcloud.ZoneRRSetTypeTXT)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get RRSet: %s\n", err)
		os.Exit(1)
	}
	if rrset == nil {
		fmt.Fprintf(os.Stderr, "RRSet not found\n")
		os.Exit(1)
	}

	// Quote value to match what was stored
	quotedValue := fmt.Sprintf(`"%s"`, validationContext)

	_, _, err = client.Zone.RemoveRRSetRecords(ctx, rrset, hcloud.ZoneRRSetRemoveRecordsOpts{
		Records: []hcloud.ZoneRRSetRecord{{Value: quotedValue}},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to remove record: %s\n", err)
		os.Exit(1)
	}
}

func Init() {

	path := os.ExpandEnv("$HOME/.tahtoken")

	fmt.Printf("Creating %s...\n", path)

	_, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create %s: %s\n", path, err)
		os.Exit(1)
	}

	fmt.Printf("Change mode of %s to 0600...\n", path)
	err = os.Chmod(path, 0600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to chmod %s: %s\n", path, err)
		os.Exit(1)
	}
}

func Test() {

	domain := mustGetDomainArg()

	err := testTokenFile()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to test token: %s\n", err)
		os.Exit(1)
	}

	token, err := getToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get token: %s\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	client := newClient(token)

	zoneName := getDomain(domain)
	fmt.Printf("Looking up zone: %s\n", zoneName)

	zone, _, err := client.Zone.GetByName(ctx, zoneName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get zone: %s\n", err)
		os.Exit(1)
	}
	if zone == nil {
		fmt.Fprintf(os.Stderr, "Zone '%s' not found in Hetzner account\n", zoneName)
		os.Exit(1)
	}

	recordName := "hcdcadclk"
	quotedValue := `"TEST-TRUENAS-ACME-HETZNER"`
	fmt.Printf("Creating TXT record: %s = %s\n", recordName, quotedValue)

	// Create test RRSet
	ttl := 3600
	_, _, err = client.Zone.CreateRRSet(ctx, zone, hcloud.ZoneRRSetCreateOpts{
		Name:    recordName,
		Type:    hcloud.ZoneRRSetTypeTXT,
		TTL:     &ttl,
		Records: []hcloud.ZoneRRSetRecord{{Value: quotedValue}},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create test record: %s\n", err)
		os.Exit(1)
	}

	// Cleanup helper (os.Exit doesn't run defers)
	cleanup := func() {
		if rs, _, err := client.Zone.GetRRSetByNameAndType(ctx, zone, recordName, hcloud.ZoneRRSetTypeTXT); err == nil && rs != nil {
			_, _, _ = client.Zone.DeleteRRSet(ctx, rs)
		}
	}

	fmt.Printf("Deleting test record...\n")

	// Get the RRSet we just created to delete it
	rrset, _, err := client.Zone.GetRRSetByNameAndType(ctx, zone, recordName, hcloud.ZoneRRSetTypeTXT)
	if err != nil {
		cleanup()
		fmt.Fprintf(os.Stderr, "Failed to get test RRSet: %s\n", err)
		os.Exit(1)
	}

	// Delete the entire RRSet
	_, _, err = client.Zone.DeleteRRSet(ctx, rrset)
	if err != nil {
		cleanup()
		fmt.Fprintf(os.Stderr, "Failed to delete test record: %s\n", err)
		os.Exit(1)
	}

	fmt.Printf("Test passed!\n")
}

func Help() {

	fmt.Printf("Usage: %s <command> <domain> <validation_name> <validation_context>\n", os.Args[0])
	fmt.Printf("\n")
	fmt.Printf("Command:\n")
	fmt.Printf("\tset\n")
	fmt.Printf("\tunset\n")
	fmt.Printf("\tinit - Initialize script\n")
	fmt.Printf("\ttest - Test script configuration\n")
	fmt.Printf("\thelp - Print help\n")

}

func main() {

	switch mustGetCommandArg() {
	case "set":
		Set()
	case "unset":
		Unset()
	case "init":
		Init()
	case "test":
		Test()
	case "help":
		Help()
	}

}
