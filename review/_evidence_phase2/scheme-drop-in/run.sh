#!/bin/bash
# run.sh <output folder> <scheme file>...: export this repository's HEAD into <output folder>, replace
# the files of the network's scheme (signing/mldsa65*.go) with the given files, and run gofmt, vet,
# build and the tests of both modules there. Run it from inside the repository.
set -u
out=$1; shift
files=(); for file in "$@"; do files+=("$(realpath "$file")"); done
rm -rf "$out" && mkdir -p "$out" && git archive HEAD | tar -x -C "$out" || exit 1
cd "$out" && rm signing/mldsa65.go signing/mldsa65_test.go signing/mldsa65_fips140v1.go && cp "${files[@]}" signing/
echo "== files changed: removed signing/mldsa65*.go (3), added $(for file in "${files[@]}"; do printf 'signing/%s ' "$(basename "$file")"; done)"
echo "== ML-DSA-65 named in Go code outside comments: $(grep -rnE 'mldsa65|MLDSA65|ML-DSA-65' --include=*.go . | grep -v '^./review' | sed -E 's|//.*$||' | grep -E 'mldsa65|MLDSA65|ML-DSA-65' | cut -d: -f1 | sort -u | tr '\n' ' ')"
export GOTOOLCHAIN=auto GOLEDGER_CHAOS_SLACK=4
gofmt -l . | grep -v '^review/'
go vet ./... && go vet -C ui ./... && go build ./... && go build -C ui -o /dev/null . && echo "== vet and build: ok" || echo "== vet or build FAILED"
go test -count=1 ./... 2>&1 | grep -E '^(ok|FAIL|---|panic)|_test\.go:[0-9]+:' | head -60
go test -C ui -count=1 ./... 2>&1 | grep -E '^(ok|FAIL|---|panic)|_test\.go:[0-9]+:' | head -40
