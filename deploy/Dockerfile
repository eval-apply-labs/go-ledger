# The go-ledger image: the engine (/server), the signer (/signer), the fetcher (/fetcher) and /keygen,
# each a static binary, in an empty image with no shell, run as the numeric user 65532, never root.
# Build it from the repository's root (make images):
#
#   docker build -f deploy/Dockerfile -t go-ledger .

# The builder: the Go version that go.mod's toolchain line names and CI uses.
FROM docker.io/library/golang:1.27.3 AS builder
WORKDIR /source
# Only the module's packages are copied, never the whole folder: deploy/quickstart holds private keys.
COPY go.mod ./
COPY consensus consensus
COPY ledger ledger
COPY node node
COPY signing signing
COPY cmd cmd
# CGO_ENABLED=0 makes each binary static, so it runs with no C library. The code uses only the
# standard library, so it costs nothing.
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server \
 && CGO_ENABLED=0 go build -o /out/signer ./cmd/signer \
 && CGO_ENABLED=0 go build -o /out/fetcher ./cmd/fetcher \
 && CGO_ENABLED=0 go build -o /out/keygen ./cmd/keygen \
 && mkdir /out/data

FROM scratch
# The certificate authorities, for a public address behind https: a CDN that the engine's bootstrap
# names, or another bank's public bundle that the engine pulls.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /out/server /out/signer /out/fetcher /out/keygen /
# /data is the engine's folder: its snapshot, sign log, record files and public folder. A new volume
# mounted here takes this folder's owner, so the engine can write to it. The fetcher mounts the same
# volume read-only, and runs as the same user, so it can read the record files, which the engine
# keeps readable by that user only.
COPY --from=builder --chown=65532:65532 /out/data /data
USER 65532:65532
