# C27-demo-tmp-key

Round-1 reports merged into this cluster (1):

## [key-lifecycle] KEY-4 — Demo writes the sole-validator private key in plaintext to a predictable os.TempDir() path with no O_EXCL/symlink safety and never deletes it
- severity/confidence (round 1): low/high
- category: key-management
- locations: cmd/demo/main.go:72, cmd/demo/main.go:136-146
- attacker: local user on the machine
- known_status: New. Prior work hardened the real wallet's at-rest sealing (keyfile.go) but the demo's own key write was not covered; the task explicitly asked about demo key paths/permissions/symlink safety.

**Description.** cmd/demo generates the founder key (sole validator, 1000 units, full governance of the demo net) and writes it, base64 in plaintext, to filepath.Join(os.TempDir(), "go_ledger_demo_wallet.json") via os.WriteFile (O_CREATE without O_EXCL, mode 0600). On a shared host os.TempDir() is /tmp (world-writable, sticky): a local attacker who pre-creates that exact fixed path can (a) make it a symlink so the write lands on a file the attacker chose (confirmed: the write follows the symlink), or (b) pre-create it owned by the attacker with readable perms, so the 0600 is not reapplied to the existing file and the attacker reads the key. The file is also never removed on Ctrl-C, leaving a plaintext key for network "village" in /tmp after the demo exits.

**Failure scenario.** On a shared machine, attacker pre-creates /tmp/go_ledger_demo_wallet.json (as a symlink into their own dir, or as a 0644 file they own). A user runs `make demo`; writeWallet writes the founder private key through/into the attacker-controlled file. While the demo runs the attacker signs payments and governance votes as the founder on the local demo net; the plaintext key also persists in /tmp afterward.

**PoC.** <scratch>/key-lifecycle/cmd/demo/poc_demo_wallet_test.go — PASS (TestPoCDemoWalletPredictablePathAndSymlinkUnsafe): confirms plaintext key write and that writeWallet follows a pre-planted symlink to an attacker-chosen target.

**Suggested fix.** Write the demo wallet into a fresh os.MkdirTemp dir (unpredictable path), open with O_CREATE|O_EXCL, and remove it on shutdown; or seal it with a demo passphrase. The real wallet (ui/keyfile.go) already does 0600 + no-silent-overwrite, so only the demo path needs this.
