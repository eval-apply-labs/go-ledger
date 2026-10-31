package ledger

import (
	"strings"
	"testing"
)

// A name has one spelling (spec 3.2): 1 to 32 lowercase ASCII letters, digits and hyphens, starting
// and ending with a letter or a digit. Every other spelling is refused, never normalised, so one
// account can never be reached under two names.
func TestCanonicalNameRefusesOtherSpellings(t *testing.T) {
	for _, name := range []string{"a", "7", "alice", "bank-a", "k-0123", "a--b", strings.Repeat("n", 32)} {
		if !CanonicalName(name) {
			t.Errorf("CanonicalName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{
		"", "-", "Alice", "ALICE", "-alice", "alice-", "al ice", " alice", "alice ", "alice\n", "al_ice", "al.ice",
		"álice", "аlice", // a Latin a with an accent, and a Cyrillic a that looks like the Latin one
		strings.Repeat("n", 33),
	} {
		if CanonicalName(name) {
			t.Errorf("CanonicalName(%q) = true, want false", name)
		}
	}
}
