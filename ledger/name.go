package ledger

import "regexp"

// canonicalNamePattern is the one spelling of a name (spec 3.2): 1 to 32 bytes of lowercase ASCII
// letters, digits and hyphens, starting and ending with a letter or a digit.
var canonicalNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// CanonicalName reports whether text is a name in its one spelling. A name is an account's identity:
// messages name their signer, and nodes look the signer's key up by name, so a name has exactly one
// spelling. Nothing is normalised: "Alice" is not a name. Neither is "-", which an account update uses
// to clear a field. It is the one check for account names and for every name a message carries.
func CanonicalName(text string) bool { return canonicalNamePattern.MatchString(text) }
