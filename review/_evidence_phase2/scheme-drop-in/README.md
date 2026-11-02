# The scheme as a drop-in replacement

The proof that the network's signature scheme is the files `signing/mldsa65*.go` and nothing else.
`run.sh` exports the repository's HEAD, deletes those three files, puts another scheme's files in
their place, and runs gofmt, vet, build and the tests of both modules. Nothing else is changed.

- `ed25519.go` and `ed25519_test.go`: Ed25519 from `crypto/ed25519`, smaller and faster than
  ML-DSA-65 (a 32-byte key, a 64-byte signature). Its `ValidatePublicKey` refuses what
  `crypto/ed25519` alone accepts: a y coordinate of p = 2^255 - 19 or above, which is a second
  encoding of a key, and the eight points of small order, under which anyone can sign. It also refuses a key of
  another size and a y no point of the curve has. `Verify` asks it before `crypto/ed25519` does any
  work. The test file checks the small-order constants with its own `math/big` curve arithmetic
  (eight distinct points, 8P the identity for each), every refusal, a thousand fresh keys taken, and
  that the scheme's `Verify` refuses a forged signature `crypto/ed25519` takes under each weak key.
- `mldsa87.go` and `mldsa87_fips140v1.go`: ML-DSA-87 from `crypto/mldsa`, larger and slower (a
  2592-byte key, a 4627-byte signature), with the same build guard as ML-DSA-65. It is `mldsa65.go`
  with the parameter set changed.
- `bigsignature.go`: a stand-in for a scheme with SLH-DSA-sized signatures: ML-DSA-65 with each
  signature padded to 20000 bytes. For the proof only.
- `ed25519.log`, `mldsa87.log`, `bigsignature.log`: the runs on this branch after the follow-ups
  of the independent review of the owner's decisions (2026-11-01): the plain address form, no
  redirect followed, the exact weak-key claims. Every package of both modules passes.

To run them, from inside the repository, each into an empty folder of its own:

```
review/_evidence_phase2/scheme-drop-in/run.sh <folder> review/_evidence_phase2/scheme-drop-in/ed25519.go review/_evidence_phase2/scheme-drop-in/ed25519_test.go
review/_evidence_phase2/scheme-drop-in/run.sh <folder> review/_evidence_phase2/scheme-drop-in/mldsa87.go review/_evidence_phase2/scheme-drop-in/mldsa87_fips140v1.go
review/_evidence_phase2/scheme-drop-in/run.sh <folder> review/_evidence_phase2/scheme-drop-in/bigsignature.go
```

The first runs found what still tied the code to ML-DSA-65's sizes: the message-size floor and default
(5000 and 6400 bytes), the wallet's bound on a receipt (16 KiB), and tests that assumed long keys. All
now follow the scheme. The files here are not part of the build: Go skips folders whose names start
with `_`.
