# TrueNAS-ACME-Hetzner

> ACME DNS-01 authenticator for TrueNAS using Hetzner DNS.
>
> **Fork of [g0rbe/truenas-acme-hetzner](https://github.com/g0rbe/truenas-acme-hetzner)**, migrated to the new Hetzner Cloud API before the [May 2026 shutdown](https://status.hetzner.com/incident/c2146c42-6dd2-4454-916a-19f07e0e5a44) of the legacy DNS Console.

[![CI](https://github.com/tkronawitter/truenas-acme-hetzner/actions/workflows/ci.yml/badge.svg)](https://github.com/tkronawitter/truenas-acme-hetzner/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/tkronawitter/truenas-acme-hetzner)](https://github.com/tkronawitter/truenas-acme-hetzner/releases)
[![codecov](https://codecov.io/gh/tkronawitter/truenas-acme-hetzner/branch/main/graph/badge.svg)](https://codecov.io/gh/tkronawitter/truenas-acme-hetzner)
[![Go Version](https://img.shields.io/github/go-mod/go-version/tkronawitter/truenas-acme-hetzner)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

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
1. Replace the binary with the new release
2. Create a new API token in Hetzner Cloud Console
3. Update `$HOME/.tahtoken` with the new token
4. [Migrate your DNS zone](https://docs.hetzner.com/networking/dns/migration-to-hetzner-console/process/) if not already done

## Prerequisites

- TrueNAS SCALE 24.x+
- Domain with DNS managed by Hetzner (migrated to Cloud Console)
- Hetzner Cloud API token with DNS permissions

## Installation

### Download Binary

```bash
wget -O /mnt/pool/tah https://github.com/tkronawitter/truenas-acme-hetzner/releases/latest/download/tah-linux-amd64
chmod +x /mnt/pool/tah
```

`releases/latest` always points to the latest stable release. Release candidates (tags like `v1.2.0-rc1`) are published as pre-releases and have to be picked by tag:

```bash
wget -O /mnt/pool/tah https://github.com/tkronawitter/truenas-acme-hetzner/releases/download/v1.2.0-rc1/tah-linux-amd64
```

### Initialize
```bash
/mnt/pool/tah init
```

## Configuration

### 1. Create API Token

> ⚠️ **You need a Hetzner Cloud Console token, NOT the old DNS Console token.**

1. Go to [Hetzner Cloud Console](https://console.hetzner.com/)
2. Select your project (or create one)
3. Go to **Security** → **API Tokens**
4. Click **Generate API Token**
5. Name it (e.g., "TrueNAS ACME") and select **Read & Write** permissions
6. Copy the token immediately (it won't be shown again)

See [Hetzner docs on generating API tokens](https://docs.hetzner.com/cloud/api/getting-started/generating-api-token/).

### 2. Store Token

```bash
echo -n "YOUR_CLOUD_API_TOKEN" > $HOME/.tahtoken
```

**Note:** File must contain only the token string, no trailing newline.

### 3. Migrate DNS Zone (if needed)

If your domain is still on the old DNS Console, [migrate it first](https://docs.hetzner.com/networking/dns/migration-to-hetzner-console/process/).

## Usage

```
tah <command> <domain> <validation_name> <validation_context>
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
- **Short TTL.** Challenge records are created with a TTL of 60 seconds, so a retry soon after a failed validation is not answered with an old cached value.
- **Timeout.** Every command gives up after 50 seconds, including the wait for Hetzner, and exits 1 with `timed out after 50s`. This stays below the TrueNAS default "Script Timeout" of 60 seconds, so the error comes from tah and is not a silent kill. Raising "Script Timeout" in TrueNAS does not raise tah's own limit.

### Test Configuration
```bash
/mnt/pool/tah test nas.example.com
```

### Check Version
```bash
/mnt/pool/tah --version
```

### TrueNAS Integration

In TrueNAS UI: **Credentials → Certificates → ACME DNS-Authenticators**
- Authenticator: **Shell**
- Script: `/mnt/pool/tah`
- Running User: see below, the default does not work
- Script Timeout: keep at least 60 seconds (the default)
- Propagation Delay: see below

**Running User.** TrueNAS runs the script as this user, and tah reads the token from that user's `$HOME/.tahtoken`. The default is `nobody`, which has no usable home directory, so the token file has nowhere to live. Do not keep the default:

1. Pick or create a user that has a home directory.
2. Log in as that user (or `su` to it) and run `tah init`, then store the token as described in [Store Token](#2-store-token).
3. As the same user, run `tah test nas.example.com`.
4. Set that user as "Running User" in the authenticator.

**Script Timeout.** Default 60 seconds, minimum 5. Keep at least 60. tah stops itself after 50 seconds and reports `timed out after 50s`. A lower value in TrueNAS kills tah in the middle of a request, without a message.

**Propagation Delay.** Default 60 seconds, minimum 10. After every `set`, TrueNAS waits this long before it asks the ACME server to validate, once for each name in the certificate. A certificate for `example.com` and `*.example.com` therefore waits twice. The delay gives Hetzner's nameservers time to serve the new record. `set` already waits until Hetzner reports the change as done, but that does not mean every nameserver serves it yet, so keep the default until you have measured how long your zone takes.

## Building from Source

Requires Go 1.26 or newer.

```bash
git clone https://github.com/tkronawitter/truenas-acme-hetzner.git
cd truenas-acme-hetzner
go build -ldflags "-X main.Version=v1.2.0" -o tah .
```

Without `-ldflags`, `tah --version` prints `dev`.

Run the tests with `go test ./...`. They use an in-memory fake of the Hetzner API and need no token or network access.

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
