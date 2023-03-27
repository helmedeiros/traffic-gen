# Dockerfile for the traffic-gen binary. Mirrors the markup-svc
# pattern (see markup-svc/cmd/markup-server/Dockerfile + ADR-0013 +
# ADR-0018 in that repo, mirrored here as ADR-0005): two-stage
# golang:1.18 build + distroless static :nonroot runtime so the
# image runs as user 65532 with no shell, no package manager, and
# no writable filesystem outside the working directory. Multi-arch
# (linux/amd64 + linux/arm64) via buildx --platform.
#
# Build context is the repo root: `docker build -f cmd/traffic-gen/Dockerfile .`

# --platform=$BUILDPLATFORM keeps the build stage on the runner's
# native architecture (no QEMU emulation during go build) while
# cross-compiling via GOARCH below. See ADR-0005.
FROM --platform=$BUILDPLATFORM golang:1.18 AS build
WORKDIR /src

ARG BUILDPLATFORM
ARG TARGETOS
ARG TARGETARCH

# Both go.mod and go.sum are copied so the module-download layer
# cache is keyed on the manifest pair (v0.0.3 added OTel deps and
# the go.sum file along with them).
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" \
    -o /out/traffic-gen ./cmd/traffic-gen

FROM gcr.io/distroless/static-debian11:nonroot
COPY --from=build /out/traffic-gen /usr/local/bin/traffic-gen

# Distroless :nonroot ships USER 65532:65532. The explicit USER line
# documents the inherited posture and survives a base-image change
# that might drop the implicit default.
USER 65532:65532

ENTRYPOINT ["/usr/local/bin/traffic-gen"]
