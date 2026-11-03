# The wallet image (ui/): one static binary in an empty image with no shell, run as the numeric user
# 65532, never root. Build it from the repository's root (make images):
#
#   docker build -f deploy/ui.Dockerfile -t go-ledger-ui .
#
# Run it with the node it reads and the network it pins. Publish its port on this machine only, and
# under the same number: the wallet refuses a request for another port, as a guard against DNS
# rebinding.
#
#   docker run -p 127.0.0.1:8090:8090 -v wallet:/wallet -v "$PWD/wallet.password":/wallet.password:ro \
#       go-ledger-ui -node https://bank-a.example -network village -password-file /wallet.password

# The builder: the Go version that go.mod's toolchain line names and CI uses.
FROM docker.io/library/golang:1.27.3 AS builder
WORKDIR /source
# The wallet module, and the packages of the root module it uses (ui/go.mod replaces it with ../).
# Only these are copied, never the whole folder: deploy/quickstart holds private keys.
COPY go.mod ./
COPY consensus consensus
COPY ledger ledger
COPY node node
COPY signing signing
COPY ui ui
RUN CGO_ENABLED=0 go build -C ui -o /out/go_ledger_ui . \
 && mkdir /out/wallet

FROM scratch
# The certificate authorities, for a bank's public address behind https.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /out/go_ledger_ui /
# /wallet holds the key file, the pin and the wallet's state. A new volume mounted here takes this
# folder's owner, so the wallet can write to it.
COPY --from=builder --chown=65532:65532 /out/wallet /wallet
USER 65532:65532
# The wallet listens on every address of the container, so that a published port reaches it; so does
# any other container on its network, and the wallet refuses to start without -password-file. Flags
# given to docker run come after these, and a flag given twice takes its last value.
ENTRYPOINT ["/go_ledger_ui", "-listen", ":8090", "-wallet", "/wallet/wallet.json"]
