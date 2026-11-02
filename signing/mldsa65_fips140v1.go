//go:build fips140v1.0

package signing

// A build that selects the FIPS 140-3 v1.0.0 module (GOFIPS140=v1.0.0, or "certified") gets a
// crypto/mldsa whose every call fails: a node could neither sign nor verify, and the wallet would
// call a valid seed an earlier network's key. This file is compiled only in such a build, and the
// line below stops it.
var _ int = "this build selects the FIPS 140-3 v1.0.0 module, which lacks crypto/mldsa"
