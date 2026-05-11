# Stage 1: Build
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git make

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 make all

# Stage 2: Runtime (distroless for minimal attack surface)
FROM gcr.io/distroless/static-debian12

# Copy binaries from builder
COPY --from=builder /src/build/swarmd-firecracker /usr/local/bin/swarmd-firecracker
COPY --from=builder /src/build/swarmcracker /usr/local/bin/swarmcracker
COPY --from=builder /src/build/swarmcracker-agent /usr/local/bin/swarmcracker-agent

# Default command
ENTRYPOINT ["swarmd-firecracker"]