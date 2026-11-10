# TrueNAS-ACME-Hetzner

> ACME DNS-01 authenticator for TrueNAS using Hetzner DNS.
>
> **Fork of [g0rbe/truenas-acme-hetzner](https://github.com/g0rbe/truenas-acme-hetzner)**, migrated to the new Hetzner Cloud API before the [May 2026 shutdown](https://status.hetzner.com/incident/c2146c42-6dd2-4454-916a-19f07e0e5a44) of the legacy DNS Console.

[![CI](https://github.com/tkronawitter/truenas-acme-hetzner/actions/workflows/ci.yml/badge.svg)](https://github.com/tkronawitter/truenas-acme-hetzner/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/tkronawitter/truenas-acme-hetzner)](https://github.com/tkronawitter/truenas-acme-hetzner/releases)
[![codecov](https://codecov.io/gh/tkronawitter/truenas-acme-hetzner/branch/main/graph/badge.svg)](https://codecov.io/gh/tkronawitter/truenas-acme-hetzner)
[![Go Version](https://img.shields.io/github/go-mod/go-version/tkronawitter/truenas-acme-hetzner)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Signed with Sigstore](https://img.shields.io/badge/signed-sigstore%2Fcosign-blue)](#verify-the-download-optional)

## Why This Fork?

Hetzner is [migrating DNS management](https://docs.hetzner.com/networking/dns/migration-to-hetzner-console/process/) from the legacy **DNS Console** (dns.hetzner.com) to the **Hetzner Cloud Console** (console.hetzner.com):

| | Legacy (upstream) | New (this fork) |
|---|---|---|
| **Console** | dns.hetzner.com | console.hetzner.com |
| **API** | Hetzner DNS API | Hetzner Cloud API |
| **Library** | `elmasy-com/elnet` | Official [`hcloud-go`](https://github.com/hetznercloud/hcloud-go) |
| **Status** | ⚠️ Shutdown May 2026 | ✅ Supported |

**Key dates:**
- **Nov 10, 2025**: New zone creation disabled on legacy console
- **May 2026**: Legacy DNS Console completely shut down

## Upgrading from Upstream

This fork is a **drop-in replacement**: same CLI interface, same config file location.

> ⚠️ **NEW API TOKEN REQUIRED**
>
> Your existing DNS Console token (`dns.hetzner.com`) **will not work**.
> You must create a new token in the [Hetzner Cloud Console](https://console.hetzner.com/).
> See [Configuration](#configuration) below.

**Steps:**
1. [Migrate your DNS zone](https://docs.hetzner.com/networking/dns/migration-to-hetzner-console/process/) to the Cloud Console, if not already done.
2. [Create a new API token](#2-create-api-token) in the project that holds the zone.
3. As the authenticator's Running User, write the new token into `$HOME/.tahtoken` (see [Store the Token](#3-choose-the-running-user-and-store-the-token)).
4. Replace the binary with the [download command](#download-binary). The path stays the same, so the authenticator needs no change.
5. Run `/mnt/pool/tah test nas.example.com` as the Running User.

**Upgrading from an earlier version of this fork:** run steps 4 and 5 only. The token and the authenticator settings stay as they are.

## Prerequisites

- TrueNAS SCALE 24.x+
- Domain with DNS managed by Hetzner (migrated to Cloud Console)
- Hetzner Cloud API token with Read & Write permission for the project that holds the zone
- x86_64 (amd64) system; only a Linux amd64 binary is released

## Installation

### Download Binary

Replace `/mnt/pool` with the path of a dataset on your pool, here and in every command below. Do not use `/tmp`: TrueNAS mounts it with `noexec`, so a binary placed there cannot be run.

```bash
wget -O /mnt/pool/tah https://github.com/tkronawitter/truenas-acme-hetzner/releases/latest/download/tah-linux-amd64
chmod +x /mnt/pool/tah
```

`releases/latest` always points to the latest stable release. A release candidate (a tag like `vX.Y.Z-rcN`) is published as a pre-release while it is being tested and is only reachable by tag. Its release page is removed once the stable release is out. To test one, replace the tag in this command (it overwrites the installed binary):

```bash
wget -O /mnt/pool/tah https://github.com/tkronawitter/truenas-acme-hetzner/releases/download/vX.Y.Z-rcN/tah-linux-amd64
```

### Verify the Download (optional)

Releases from v1.3.0-rc1 on carry a Sigstore bundle (`tah-linux-amd64.sigstore.json`) created by the release workflow. It holds the signature, the signing certificate and the transparency log proof in one file, so the check works offline. You need [cosign](https://github.com/sigstore/cosign) 3.x or newer. Set `VERSION` to the release tag, download the binary and the bundle into one directory, and run the check there. Run it on any machine that has cosign; the check does not need to run on the NAS, and you can copy the binary to the NAS afterwards.

```bash
VERSION=v1.3.0
wget https://github.com/tkronawitter/truenas-acme-hetzner/releases/download/${VERSION}/tah-linux-amd64
wget https://github.com/tkronawitter/truenas-acme-hetzner/releases/download/${VERSION}/tah-linux-amd64.sigstore.json
cosign verify-blob \
  --bundle tah-linux-amd64.sigstore.json \
  --certificate-identity "https://github.com/tkronawitter/truenas-acme-hetzner/.github/workflows/release.yml@refs/tags/${VERSION}" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  tah-linux-amd64
```

Expected output: `Verified OK`. The release workflow runs the same check before it publishes anything.

**Older releases (v1.2.0 and earlier)** carry a detached signature (`tah-linux-amd64.sig`) and certificate (`tah-linux-amd64.pem`) instead of the bundle. The first releases with a bundle (up to and including v1.3.0) still publish both forms; the release after v1.3.0 drops the detached files. For a release that has them:

```bash
VERSION=v1.2.0
wget https://github.com/tkronawitter/truenas-acme-hetzner/releases/download/${VERSION}/tah-linux-amd64
wget https://github.com/tkronawitter/truenas-acme-hetzner/releases/download/${VERSION}/tah-linux-amd64.sig
wget https://github.com/tkronawitter/truenas-acme-hetzner/releases/download/${VERSION}/tah-linux-amd64.pem
cosign verify-blob \
  --certificate tah-linux-amd64.pem \
  --signature tah-linux-amd64.sig \
  --certificate-identity "https://github.com/tkronawitter/truenas-acme-hetzner/.github/workflows/release.yml@refs/tags/${VERSION}" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  tah-linux-amd64
```

Expected output: `Verified OK`. cosign 3.x prints a deprecation warning for `--signature` and `--certificate`; the check still works.

## Configuration

### 1. Migrate DNS Zone (if needed)

If your domain is still on the old DNS Console, [migrate it first](https://docs.hetzner.com/networking/dns/migration-to-hetzner-console/process/). The token in the next step has to come from the project that holds the migrated zone.

### 2. Create API Token

> ⚠️ **You need a Hetzner Cloud Console token, NOT the old DNS Console token.**

1. Go to [Hetzner Cloud Console](https://console.hetzner.com/)
2. Select your project (or create one)
3. Go to **Security** → **API Tokens**
4. Click **Generate API Token**
5. Name it (e.g., "TrueNAS ACME") and select **Read & Write** permissions
6. Copy the token immediately (it won't be shown again)

See [Hetzner docs on generating API tokens](https://docs.hetzner.com/cloud/api/getting-started/generating-api-token/).

### 3. Choose the Running User and Store the Token

TrueNAS runs tah as the authenticator's "Running User" and sets `$HOME` to that user's home directory. tah reads the token from `$HOME/.tahtoken`. The default user `nobody` has no usable home directory, so the token file has nowhere to live: do not use it. Run the commands below as the user you will set as "Running User". The examples use `acme` as a placeholder for that user.

1. Pick or create a user that has a home directory.
2. Create the token file with mode 0600:

```bash
sudo -u acme -H /mnt/pool/tah init
```

3. Write the token into it:

```bash
sudo -u acme -H sh -c 'echo -n "YOUR_CLOUD_API_TOKEN" > $HOME/.tahtoken'
```

4. Check the setup (creates and deletes a test TXT record in your zone):

```bash
sudo -u acme -H /mnt/pool/tah test nas.example.com
```

The user must be able to read and execute the binary. If you switch to the user in some other way, make sure `$HOME` points to its home directory.

**Note:** The file must contain only the token string. Spaces, tabs and line endings at the start and end are removed when the file is read, so an editor that adds a line feed or Windows line ending (CRLF) does no harm. An empty file stops with `token file <path> is empty, put your Hetzner Cloud API token in it`.

## Usage

```
tah set|unset <domain> <validation_name> <validation_context>   # called by TrueNAS
tah test <domain>                                                # run by you
tah init | version | --version | help
```

| Command | Purpose |
|---|---|
| `set` | Add the ACME challenge TXT record (called by TrueNAS) |
| `unset` | Remove the ACME challenge TXT record (called by TrueNAS) |
| `init` | Create `$HOME/.tahtoken` with mode 0600 |
| `test` | Check the token file and create and delete a test TXT record |
| `version`, `--version` | Print the version |
| `help` | Print help |

Exit code 0 means success, 1 means failure; TrueNAS only looks at the exit code.

### How `set` and `unset` behave

- **They return once the change is live.** Hetzner applies DNS changes asynchronously. `set` and `unset` wait until Hetzner reports the change as done, so TrueNAS does not ask for validation too early. If Hetzner reports the change as failed, the command exits 1.
- **They are safe to repeat.** `set` with a value that is already there changes nothing and exits 0. `unset` exits 0 when the value or the whole record set is already gone, so a repeated cleanup does not fail the certificate job.
- **Several values per name work.** A certificate for `example.com` and `*.example.com` puts two values under `_acme-challenge.example.com`. Both are kept until each is removed. Removing the last value deletes the record set.
- **Short TTL.** Record sets that tah creates get a TTL of 60 seconds (an existing record set keeps its TTL), so a retry soon after a failed validation is not answered with an old cached value.
- **They retry temporary failures.** If Hetzner answers with a server error (5xx), a rate limit, a lock or conflict, or an "already exists" error, or if the connection fails, `set` and `unset` try again, up to 3 attempts within 40 seconds. Each retry prints `Attempt N of 3 failed, retrying in Xs: ...` to stderr. Authentication errors (401, 403), not found (404) and invalid input fail at once, because another try would not change the answer. `test` is not repeated.
- **`set` checks the nameservers.** After the change, `set` asks every authoritative nameserver of the zone directly for the TXT value and waits at most 30 seconds for all of them to serve it, within the 50 seconds below. If `set` needed its retries, the check gets less, but at least 10 seconds. The check only warns: `set` never fails because of it, and `unset` does not check. It needs outbound UDP and TCP port 53 to the Hetzner nameservers; without it the check has nothing to report. It prints to stdout:
  - `Challenge served by N of M nameservers after Xs`: all reachable nameservers serve the value. N is lower than M when some nameservers could not be queried.
  - `Warning: after Xs the challenge is still missing on <nameservers>; continuing, the TrueNAS propagation delay and the ACME validation decide`: the wait ran out. `set` still exits 0.
  - `Warning: cannot query <nameserver>, checking the other nameservers only`: one or more nameservers did not answer; the others are checked.
  - `Warning: nameserver check skipped, cannot query <nameservers> (is outbound port 53 blocked?): <reason>`: no nameserver could be queried, so the check is skipped.
  - `Nameserver check skipped: not enough time left before the 50s timeout`: the retries used up the time.
  - `Nameserver check skipped: the zone lists no authoritative nameservers`: Hetzner returned no nameservers for the zone.
- **Timeout.** `set` and `unset` give up after 50 seconds, including the wait for Hetzner, and exit 1 with `timed out after 50s`. See Script Timeout under [TrueNAS Integration](#truenas-integration).

### Which Zone Is Used

`set` and `unset` write into the most specific zone in your Hetzner account that contains the `validation_name`. The `<domain>` argument is only checked for being a valid name and plays no part in the choice. tah tries the parent names of the `validation_name` one after the other, from the most specific one up to the registrable domain (for example `example.com` or `example.co.uk`), and takes the first one that exists as a zone in the account.

With the zones `example.com` and `dev.example.com` in the account, `_acme-challenge.nas.dev.example.com` goes into `dev.example.com`, and `_acme-challenge.nas.example.com` goes into `example.com`.

Because the zone follows the `validation_name`, the certificate's domain and the zone can differ. This is what a CNAME delegation of `_acme-challenge` into another zone of the same Hetzner account needs: when the `validation_name` lies in that other zone, tah writes there.

tah prints the result for every `set` and `unset`:

```
Zone dev.example.com, record _acme-challenge.nas
```

If no candidate is a zone in the account, the command exits 1 with `no zone in the Hetzner account matches (tried ...)`, where the brackets list the names it tried, separated by commas. Either the token belongs to a different Hetzner project than the zone, or the zone does not exist in the account.

**Known limit (#40).** tah only knows the zones in your Hetzner account. If a subdomain is delegated to another DNS provider and the account holds only the parent zone, tah writes into the parent zone, and the validation fails because the delegated name servers never see the record. tah does not detect this.

### Test Configuration (recommended)

Run this as the Running User, after every change to the token or the user. It needs only a domain in your zone.

```bash
sudo -u acme -H /mnt/pool/tah test nas.example.com
```

`test` looks for the zone of the domain you pass, then for its parent names, and reports what it found:

```
Looking up zone for: nas.example.com
Found zone: example.com
```

### Check Version
```bash
/mnt/pool/tah --version
```

### TrueNAS Integration

In TrueNAS UI: **Credentials → Certificates → ACME DNS-Authenticators**
- Authenticator: **Shell**
- Script: `/mnt/pool/tah` (required)
- Running User: the user from [step 3](#3-choose-the-running-user-and-store-the-token) (required; the default `nobody` does not work)
- Script Timeout: leave at the default of 60 seconds or higher
- Propagation Delay: leave at the default of 60 seconds (see below)

**Script Timeout.** Default 60 seconds, minimum 5. Keep at least 60. tah stops itself after 50 seconds and reports `timed out after 50s`, which is below the default, so the error comes from tah and is not a silent kill. A lower value in TrueNAS kills tah in the middle of a request, without a message. Raising the value in TrueNAS does not raise tah's own 50 second limit.

**Propagation Delay.** Default 60 seconds, minimum 10. Keep the default until you have measured how long your zone takes.

- TrueNAS waits this long after every `set`, once for each name in the certificate. A certificate for `example.com` and `*.example.com` therefore waits twice.
- `set` returns when Hetzner reports the change as done. That does not mean every Hetzner nameserver serves the record yet; the delay covers that gap. `set` also checks the nameservers for up to 30 seconds (see [How `set` and `unset` behave](#how-set-and-unset-behave)), but that check only warns and does not replace the delay. In tests the slowest Hetzner nameserver served a new value 16 to 23 seconds after `set` started, so the default of 60 seconds leaves room.

## Building from Source

Requires Go 1.26 or newer.

```bash
git clone https://github.com/tkronawitter/truenas-acme-hetzner.git
cd truenas-acme-hetzner
go build -ldflags "-X main.Version=v1.2.0" -o tah .
```

Without `-ldflags`, `tah --version` prints `dev`.

Run the tests with `go test ./...`. They use a local fake Hetzner API server and need no token or internet access.

## Maintenance

### Dependency Updates

Dependabot checks Go modules and GitHub Actions once a month, on the 1st at 06:00 UTC. The policy lives in `.github/dependabot.yml`.

- Minor and patch updates of one ecosystem arrive together in one pull request. Every major update gets its own pull request.
- Dependabot waits after a release before proposing it. Go modules wait 7 days, 14 days for a major release. GitHub Actions always wait 7 days, because Dependabot has no separate major cooldown for that ecosystem.
- Commit titles start with `deps` (Go modules) or `ci` (GitHub Actions).
- `sigstore/cosign-installer` updates arrive like other GitHub Actions. The action's version fixes the cosign version used for signing, so a bump moves both together.

**Monthly routine**

1. Dependabot opens the pull requests.
2. Wait for CI to pass.
3. Merge each pull request by hand. There is no auto-merge.

**Releases.** Dependency updates collect on `main` and ship with the next feature release. An update to GitHub Actions never triggers a release. A patch release outside the schedule happens only when `govulncheck` reports a vulnerability that the binary can reach. The TrueNAS test runs once per release, not once per Dependabot pull request.

### Repository Settings

The two settings below enable Dependabot alerts and security updates. They are repository settings, not part of any file in the repository. Run them once with the GitHub CLI:

```bash
gh api -X PUT repos/tkronawitter/truenas-acme-hetzner/vulnerability-alerts
gh api -X PUT repos/tkronawitter/truenas-acme-hetzner/automated-security-fixes
```

To switch them off again, send `-X DELETE` to the same URLs.

**Forks.** In a fork, Dependabot version updates are off until you switch them on under **Insights**, **Dependency graph**, **Dependabot**, with **Enable Dependabot**. This cannot be done through the API. Until then Dependabot does not run, even with a valid `.github/dependabot.yml`.

## References

- [Hetzner DNS Migration Docs](https://docs.hetzner.com/networking/dns/migration-to-hetzner-console/process/)
- [Hetzner Cloud API Token Guide](https://docs.hetzner.com/cloud/api/getting-started/generating-api-token/)
- [hcloud-go Library](https://github.com/hetznercloud/hcloud-go)
- [TrueNAS Shell Authenticator Source](https://github.com/truenas/middleware/blob/master/src/middlewared/middlewared/plugins/acme_dns_authenticator/authenticators/shell.py)
- [TrueNAS ACME Issuance Loop](https://github.com/truenas/truenas_crypto_utils/blob/master/truenas_acme_utils/issue_cert.py)
- [Original Project](https://github.com/g0rbe/truenas-acme-hetzner)

## License

MIT License - see [LICENSE](LICENSE) for details.

Originally created by [Dániel Görbe](https://github.com/g0rbe).
