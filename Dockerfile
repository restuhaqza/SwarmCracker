# The daemon image: swarmd-firecracker, plus the CLI and the test agent.
#
# Build (or `make docker-image`):
#   docker build -t swarmcracker:dev .
#   docker build --build-arg VERSION=v0.11.0 --build-arg GIT_COMMIT=$(git rev-parse --short HEAD) \
#     -t swarmcracker:v0.11.0 .
#
# Every binary is built with an explicit `go build` into /out. `make` is
# deliberately not used: its `swarmd-firecracker` and `swarmcracker-agent`
# targets are file targets, so a same-named file anywhere in the build context
# (a stray ./swarmd-firecracker from a local build, say) makes make answer
# "up to date", build nothing, and the image then fails at COPY time with
# "/src/build/swarmd-firecracker: not found". Building by package path cannot be
# shadowed by a file.
#
# CGO is off and the output is non-PIE on purpose. The runtime layer is
# distroless/static, which ships no dynamic loader; a PIE binary — the
# Makefile's `-buildmode=pie` default — cannot exec there. `-buildmode=exe` plus
# `CGO_ENABLED=0` gives a static, executable ELF.
FROM golang:1.26-alpine AS build

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_TIME=unknown

RUN CGO_ENABLED=0 go build -trimpath -buildmode=exe \
	-ldflags "-s -w -X main.Version=${VERSION}" \
	-o /out/swarmd-firecracker ./cmd/swarmd-firecracker

RUN CGO_ENABLED=0 go build -trimpath -buildmode=exe \
	-ldflags "-s -w -X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME} -X main.GitCommit=${GIT_COMMIT}" \
	-o /out/swarmcracker ./cmd/swarmcracker

RUN CGO_ENABLED=0 go build -trimpath -buildmode=exe \
	-ldflags "-s -w -X main.Version=${VERSION}" \
	-o /out/swarmcracker-agent ./cmd/swarmcracker-agent

# Runtime: distroless static, no shell and no package manager. It stays root
# (unlike Dockerfile.cli) because the daemon opens /dev/kvm, creates TAP
# devices and manages bridges on the host; the microVMs themselves are confined
# by KVM and the guest kernel, not by this uid.
FROM gcr.io/distroless/static-debian12

COPY --from=build /out/swarmd-firecracker /usr/local/bin/swarmd-firecracker
COPY --from=build /out/swarmcracker /usr/local/bin/swarmcracker
COPY --from=build /out/swarmcracker-agent /usr/local/bin/swarmcracker-agent

ENTRYPOINT ["/usr/local/bin/swarmd-firecracker"]
