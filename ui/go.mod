module github.com/eval-apply-labs/go-ledger/ui

go 1.27

toolchain go1.27.3

require github.com/eval-apply-labs/go-ledger v0.0.0-00010101000000-000000000000

// The wallet UI is a separate codebase (its own module/executable) but currently lives beside
// the server for convenience. This replace points at the co-located server module; when the UI
// is moved to its own repo, drop the replace and require a published go_ledger version.
replace github.com/eval-apply-labs/go-ledger => ../
