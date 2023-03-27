# 5. Multi-arch (linux/amd64 + linux/arm64) image publish

## Status

Accepted — `cmd/traffic-gen/Dockerfile` builds with `--platform=$BUILDPLATFORM` on the build stage and cross-compiles via `GOARCH=${TARGETARCH:-amd64}`; the CI image-publish job passes `platforms: linux/amd64,linux/arm64` to `docker/build-push-action` so every published tag is a manifest list. Mirror of markup-svc/ADR-0018 — same problem, same fix, same posture (Dockerfile keeps single-arch `docker build` working as the default, multi-arch ships via buildx).

## Context

The cross-service trace instrumented across the platform (traffic-gen ADR-0004 + decision-gateway ADR-0002 + markup-svc ADR-0017) measured ~1.7ms of network round-trip + connection-pool overhead between traffic-gen → gateway → markup-svc in a 2.0ms total request. On Apple Silicon dev boxes pulling the platform's published amd64 images, the bulk of that 1.7ms is Rosetta-2 emulation, not actual wire time. With multi-arch images the trace's per-hop network cost becomes representative of native performance, and the operator's bottleneck investigation becomes meaningful.

Two design questions; the rationale lives in `markup-svc/ADR-0018`. Quick recap so this ADR stands on its own:

1. **Cross-compile vs QEMU emulation.** Pick cross-compile. `--platform=$BUILDPLATFORM` on FROM + `GOARCH=$TARGETARCH` on `go build` keeps the build stage native (no QEMU slowdown).
2. **Manifest list vs per-arch tags.** Pick manifest list. `docker pull ghcr.io/helmedeiros/traffic-gen:vN` resolves to the matching arch automatically; no operator-visible change.

## Decision

`cmd/traffic-gen/Dockerfile`:

- Build stage `FROM` gains `--platform=$BUILDPLATFORM`.
- `ARG BUILDPLATFORM`, `ARG TARGETOS`, `ARG TARGETARCH` declared.
- `go build` invocation uses `GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64}` (defaults preserve `docker build` without buildx).
- `COPY go.mod go.sum ./` — the placeholder comment from the v0.0.1 / v0.0.2 era ("go.sum lands in a future release") is now stale; OTel deps added in v0.0.3 brought go.sum in. The COPY pair stays the right shape.

`.github/workflows/ci.yml` image job:

- `docker/build-push-action@v5` step gains `platforms: linux/amd64,linux/arm64`.
- `docker/setup-buildx-action@v3` already present from the original image job.

## Consequences

### Closed by this ADR

- `docker pull ghcr.io/helmedeiros/traffic-gen:vN` on Apple Silicon returns the arm64 variant; the `decision-gateway/docker-compose.yaml` cookbook recipes work natively without Rosetta-2 emulation.
- arm64 production targets (Graviton) are unlocked.
- The `:vN` tag stays the canonical reference everywhere; compose files + cookbook recipes do not change.

### NOT closed by this ADR

- linux/arm/v7 (32-bit ARM) is not in the platforms list. Lands when an operator's deployment target asks; cross-compile path already handles GOARCH=arm.
- Per-platform image-size budget assertion. Lands if regression becomes a problem.

### Performance impact

- CI build time: +30 seconds vs the original amd64-only build (one extra cross-compile invocation). Cache hits on subsequent runs keep steady-state close to the original.
- Pull time on Apple Silicon: drops Rosetta-2 startup + per-syscall translation overhead. The dev-stack trace's per-hop network cost drops to actual Docker-bridge wire time.
- Runtime: zero difference between native amd64 and native arm64. Improvement is purely removal of the emulation layer.

### Validation strategy

- Local: `docker buildx build --platform linux/amd64,linux/arm64 -f cmd/traffic-gen/Dockerfile .` produces a manifest list. Verify via `docker buildx imagetools inspect`.
- CI: builds + pushes both arches on main + tag. `docker pull` on Apple Silicon resolves to arm64.
- Integration smoke: bring up the canonical compose stack on Apple Silicon native; observe the absence of the platform-mismatch warning; observe the per-hop trace cost dropping ~10x.
