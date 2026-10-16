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
- [TrueNAS Shell Authenticator Source](https://github.com/truenas/middleware/blob/master/src/middlewared/middlewared/plugins/acme_protocol_/authenticators/shell.py)
- [Original Project](https://github.com/g0rbe/truenas-acme-hetzner)

## License

MIT License - see [LICENSE](LICENSE) for details.

Originally created by [Dániel Görbe](https://github.com/g0rbe).
