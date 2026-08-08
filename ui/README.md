# go_ledger_ui — wallet

A friendly local wallet for a go_ledger network. It holds your key on your own machine, shows your
balance and the accounts on the ledger, and lets you send a payment with a simple form — instead of
editing JSON and signatures by hand (as the original required).

This is a **separate codebase / executable** from the ledger server. It currently lives beside the
server as its own Go module under `ui/`; it can be moved into its own repo unchanged (drop the
`replace` in `go.mod` and require a published `go_ledger` version).

## Build & run

    go build -o wallet .
    ./wallet -node http://<a-node>:8080 -network <networkId> -wallet wallet.json -listen 127.0.0.1:8090

Then open <http://127.0.0.1:8090> in a browser.

Flags:

| flag | meaning | default |
|---|---|---|
| `-node` | base URL of any node on the network | `http://127.0.0.1:8080` |
| `-network` | the network id (must match the node's) | `village` |
| `-wallet` | your key file; created on first run if absent | `wallet.json` |
| `-listen` | where to serve the wallet UI (keep it local) | `127.0.0.1:8090` |
| `-lookahead` | blocks ahead a payment names (raise if payments miss their block) | `3` |

## How it works

- On first run it generates an Ed25519 key pair and saves it to the wallet file (mode `0600`). The
  public key is your account; the private key is your secret and **never leaves your machine**.
- It reads balances and the account list from the node over HTTP (`/hdls/accounts`, `/hdls/status`).
- To send, it builds a payment, signs it locally (domain `payment`, bound to the network) with
  `ledger.SignPayment`, and POSTs only the *signed* payment to the node (`/hdls/pay`).

See [USER_MANUAL.md](USER_MANUAL.md) for a non-technical end-user walkthrough.

## Running as a service (no terminal)

The key file's passphrase is asked on a terminal. When stdin is not a terminal (systemd, nohup, a
container), the wallet reads the passphrase from the **`GOLEDGER_WALLET_PASSPHRASE`** environment
variable instead. Without it, a *new* key is stored **unencrypted** (a warning is printed to
stderr), and an already-sealed key refuses to open with a hint to set the variable. Set it in the
service environment to keep the key sealed:

    Environment=GOLEDGER_WALLET_PASSPHRASE=…   # systemd unit

The wallet also keeps a small `<wallet>.state` sidecar next to the key file (a payment counter that
prevents same-block double-issue after a restart) — back up and restore the two files together.

## Notes / TODO

- Recipients are entered as a registered *name* (like `bakery`) or a raw public key. A name is
  signed into the payment itself and resolved on-chain by every node against the committed
  registry — the wallet's own node never picks the recipient.
- **Replay protection:** a payment names a target block (`BlockID`, plus the network id in the
  signature) and is applied only in that exact block, so a resubmitted, already-cleared payment is
  stale and never re-applied. The wallet names a block a few ahead (`-lookahead`); raise it if
  payments miss their block on a fast network.
- The node returns a signed **receipt** on submission (proof it accepted and queued the payment);
  the wallet verifies it — node signature, PaymentID match, and that the signer is a current
  validator — and shows "receipt … verified ✓" only when all three hold.
- Single-account wallet; multi-account and an explicit key import/export flow are future work.
