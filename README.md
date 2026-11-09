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

`releases/latest` always points to the latest stable release. Release candidates (tags like `v1.2.0-rc1`) are pre-releases and are only reachable by tag. To test one, replace the tag in this command (it overwrites the installed binary):

```bash
wget -O /mnt/pool/tah https://github.com/tkronawitter/truenas-acme-hetzner/releases/download/v1.2.0-rc1/tah-linux-amd64
```

### Verify the Download (optional)

Each release carries a Sigstore signature (`tah-linux-amd64.sig`) and certificate (`tah-linux-amd64.pem`) created by the release workflow. To check the binary with [cosign](https://github.com/sigstore/cosign), set `VERSION` to the release tag, download the binary and both files into one directory, and run the check there. Run it on any machine that has cosign; the check does not need to run on the NAS, and you can copy the binary to the NAS afterwards.

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

**Note:** The file must contain only the token string. Line feeds at the start and end are removed when the file is read, so an editor that adds one does no harm. Windows line endings (CRLF) are not removed and make the token invalid.

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
- **Timeout.** `set` and `unset` give up after 50 seconds, including the wait for Hetzner, and exit 1 with `timed out after 50s`. See Script Timeout under [TrueNAS Integration](#truenas-integration).

### Test Configuration (recommended)

Run this as the Running User, after every change to the token or the user. It needs only a domain in your zone.

```bash
sudo -u acme -H /mnt/pool/tah test nas.example.com
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
- `set` returns when Hetzner reports the change as done. That does not mean every Hetzner nameserver serves the record yet; the delay covers that gap.

## Building from Source

Requires Go 1.26 or newer.

```bash
git clone https://github.com/tkronawitter/truenas-acme-hetzner.git
cd truenas-acme-hetzner
go build -ldflags "-X main.Version=v1.2.0" -o tah .
```

Without `-ldflags`, `tah --version` prints `dev`.

Run the tests with `go test ./...`. They use a local fake Hetzner API server and need no token or internet access.

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
