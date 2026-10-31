# C23-wallet-env-passphrase-noop

Round-1 reports merged into this cluster (1):

## [wallet-ui] KEY-2 — Setting GOLEDGER_WALLET_PASSPHRASE as instructed does NOT seal an existing plaintext wallet — key stays in plaintext, silently
- severity/confidence (round 1): medium/high
- category: key-management
- locations: ui/keyfile.go:90, ui/keyfile.go:228-237, ui/README.md:40-47, ui/USER_MANUAL.md:31
- attacker: local user on the machine, or anyone who obtains a backup/disk image of wallet.json
- known_status: new — findings 'Third pass' records the sealing feature and the env var for services as the fix for plaintext storage; the gap that the env var does not upgrade an existing plaintext file (and suppresses the warning thereafter) is not recorded.

**Description.** On a non-interactive first run the wallet stores the Ed25519 private key in base64 PLAINTEXT and warns 'Set GOLEDGER_WALLET_PASSPHRASE to seal it' (keyfile.go:236); README repeats 'Set it ... to keep the key sealed'. But the plaintext→sealed upgrade is gated by `interactive() && os.Getenv(passphraseEnv) == ""` (keyfile.go:90): when the operator follows the advice and sets the env var, the upgrade branch is skipped and the file is left plaintext — and because the run is now non-interactive with the var set, no further warning is emitted on any later start. The env var only ever seals a FRESH key (newPassphrase at creation); it never seals an already-created plaintext one. So the documented remediation is a silent no-op, leaving a validator/governance key readable by anyone who can read the file or a backup.

**Failure scenario.** Operator runs the wallet as a service once without the var (plaintext + warning), reads the warning, sets GOLEDGER_WALLET_PASSPHRASE in the unit, restarts. PoC: create wallet with empty env (plaintext), set env to a passphrase, reload — wallet.json still has privateKey in plaintext (Encrypted==nil). Operator believes the key is sealed; it is not.

**PoC.** <scratch>/wallet-ui/ui/poc_keyfile_test.go (TestPoCEnvPassphraseDoesNotSealExistingPlaintext) — FAILS: 'after setting GOLEDGER_WALLET_PASSPHRASE as instructed, the wallet file still holds the private key in PLAINTEXT'.

**Suggested fix.** When a plaintext key loads and a passphrase is available (env var OR interactive), re-seal in place and rewrite the file atomically — drop the `os.Getenv(passphraseEnv) == ""` condition from the upgrade gate. At minimum, keep printing the 'stored UNENCRYPTED' warning on every start while the on-disk form is plaintext, even when the env var is set, so the operator is not misled.
