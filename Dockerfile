# Minimal distroless image for pi5_exporter.
#
# The exporter is a single static (CGO_ENABLED=0) Go binary, so the runtime
# image is the distroless "static" base — no shell, no package manager, no libc,
# runs as a non-root user.
#
# Build (podman, native arm64 on a Pi 5):
#   podman build -t pi5_exporter:dev \
#     --build-arg VERSION=$(git describe --tags --always --dirty) \
#     --build-arg REVISION=$(git rev-parse --short HEAD) \
#     --build-arg BRANCH=$(git rev-parse --abbrev-ref HEAD) \
#     --build-arg BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) .
#
# Run (needs the firmware mailbox device and the host 'video' group):
#   podman run --rm -p 2712:2712 \
#     --device /dev/vcio \
#     --group-add keep-groups \      # podman: keep the invoking user's groups
#     pi5_exporter:dev
#   # ...or pass the host's video GID explicitly: --group-add <video-gid>
#
# The container runs as non-root (uid 65532); --group-add gives it the 'video'
# group so it can open /dev/vcio. Append exporter flags after the image name.

# ---- build stage -----------------------------------------------------------
# Pinned to match go.mod's `toolchain go1.26.4`. --platform=$BUILDPLATFORM keeps
# the compiler running on the native build arch even for cross-builds; the Go
# toolchain then cross-compiles to $TARGETARCH (free for a static binary).
FROM --platform=$BUILDPLATFORM golang:1.26.4-bookworm AS build

WORKDIR /src

# Download modules first so this layer caches unless go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

# Build.
COPY . .
ARG TARGETOS TARGETARCH
ARG VERSION=dev REVISION=unknown BRANCH=unknown BUILD_DATE=
ENV CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH}
RUN go build -trimpath \
      -ldflags="-s -w \
        -X github.com/prometheus/common/version.Version=${VERSION} \
        -X github.com/prometheus/common/version.Revision=${REVISION} \
        -X github.com/prometheus/common/version.Branch=${BRANCH} \
        -X github.com/prometheus/common/version.BuildUser=container \
        -X github.com/prometheus/common/version.BuildDate=${BUILD_DATE}" \
      -o /pi5_exporter .

# ---- runtime stage ---------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="pi5_exporter" \
      org.opencontainers.image.description="Prometheus exporter for Raspberry Pi 5 (BCM2712) firmware metrics" \
      org.opencontainers.image.source="https://github.com/bcrisp4/pi5_exporter" \
      org.opencontainers.image.licenses="Apache-2.0"

COPY --from=build /pi5_exporter /usr/local/bin/pi5_exporter

EXPOSE 2712
# Already non-root via the :nonroot base (uid 65532); stated explicitly here.
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/pi5_exporter"]
