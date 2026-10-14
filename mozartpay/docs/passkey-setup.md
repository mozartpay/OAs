# Passkey Protection Setup Guide

This guide covers WebAuthn passkey protection for Stellar wallets in MozartPay CLI.

## Overview

Passkey protection locks a normal Stellar wallet with a WebAuthn passkey
(Touch ID, Face ID, Windows Hello, or a security key). The wallet stays a
regular Ed25519 Stellar wallet — the passkey protects the seed, it does not
sign Stellar transactions itself.

### Security model

- The seed is stored **AES-256-GCM-encrypted** in the wallet file
  (`privateKeyEnc`); the plaintext seed is removed.
- The encryption key lives in the **OS keychain** (macOS Keychain, Windows
  Credential Manager, Linux Secret Service) under the `mozartpay` service.
- A **verified WebAuthn assertion** in your browser unlocks a short-lived
  grant (default 15 minutes) that lets the CLI sign while you are present.
- When the grant expires — or after `wallet passkey lock` — the wallet cannot
  sign. All signing paths fail with an "unlock first" error.
- Passkey credentials are stored at
  `~/.mozartpay/state/passkeys/<address>.json` with `0600` permissions.
- The WebAuthn ceremony runs on an ephemeral `localhost` HTTP server with a
  random session token; no external server or `wwwallet.app` dependency.

## Prerequisites

- An existing Stellar wallet (`wallet connect --provider stellar` or
  `wallet import`)
- A browser with WebAuthn support
- A working OS keychain (on headless Linux, a Secret Service daemon such as
  `gnome-keyring` or `keepassxc` must be running)

## Quick Start

```bash
# 1. Create a Stellar wallet
mozartpay wallet connect --provider stellar --network stellar-testnet

# 2. Enable passkey protection (opens a browser ceremony)
mozartpay wallet passkey register

# 3. Unlock before signing (opens a browser ceremony)
mozartpay wallet passkey unlock

# 4. Check status
mozartpay wallet passkey status
```

## Commands

All commands accept `--address <addr>`; the active wallet is used when omitted.

### `wallet passkey register`

Registers a WebAuthn credential, generates a random encryption key, stores the
key in the OS keychain, encrypts the seed, and removes the plaintext seed from
the wallet file.

```bash
mozartpay wallet passkey register
mozartpay wallet passkey register --address GABC...
```

### `wallet passkey unlock`

Runs a WebAuthn assertion ceremony. On success the decrypted seed is kept in
the OS keychain for the TTL (default `15m`).

```bash
mozartpay wallet passkey unlock
mozartpay wallet passkey unlock --ttl 1h
```

### `wallet passkey lock`

Immediately drops the unlock grant.

```bash
mozartpay wallet passkey lock
```

### `wallet passkey status`

Shows whether the wallet is protected and whether an unlock grant is live.

```bash
mozartpay wallet passkey status
```

### `wallet passkey list`

Lists all passkey-protected wallets with their credentials and lock state.

```bash
mozartpay wallet passkey list
```

### `wallet passkey disable`

Requires a successful assertion, then restores the plaintext seed and deletes
all passkey state (credentials, keychain entries). Use `--confirm` to skip the
prompt.

```bash
mozartpay wallet passkey disable
```

## Signing with a protected wallet

Every signing path (contract deploy/invoke, asset issuance, DID attestation,
payments, `wallet export --private`) resolves the seed through the unlock
grant. If the wallet is locked, you will see:

```
wallet GABC... is passkey-protected and locked — run 'mozartpay wallet passkey unlock GABC...'
```

## Troubleshooting

**"could not store encryption key in OS keychain"** — on headless Linux no
Secret Service daemon is running. Start `gnome-keyring`/`keepassxc`, or run in
a session with a keychain available.

**Browser doesn't open** — the CLI always prints the ceremony URL; copy it into
a WebAuthn-capable browser on the same machine. The link is loopback-only and
token-protected.

**"no passkey registered for wallet"** — run `wallet passkey register` first,
or check `wallet passkey status` to see the protection state.

**Unlock grant expired** — grants are time-limited by design. Run
`wallet passkey unlock` again (optionally with a longer `--ttl`).

## Migrating from the wwWallet demo

Legacy wallets created by the removed wwWallet scaffold (`type: "wwwallet"`)
are migrated to `external` wallets automatically on load — they never held
real signing keys. Passkeys are now a protection layer on real Stellar
wallets; create a Stellar wallet and run `wallet passkey register` to use them.
