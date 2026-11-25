# The scaler: a static Go binary on distroless (no shell, no package manager), non-root.
#
#   docker build .                   compiles in Docker (local use)
#   docker build --target prebuilt . packs a binary built beforehand at out/scaler (CI:
#                                    .github/workflows/scaler.yaml compiles outside Docker)

# The build stage runs on the build machine's own platform and cross-compiles (no emulation).
FROM --platform=$BUILDPLATFORM golang:1.27.4@sha256:5f1b98fa02fe396ebce2a0227a0e08309e11792915b9dd2e1f71053d7e7e6666 AS build
WORKDIR /src
# Modules first: this layer stays cached while only the code changes.
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /scaler ./cmd/scaler

FROM gcr.io/distroless/static-debian13:nonroot@sha256:8ce6dd87e9823c5b48dcfb2c8604714ccb351ce59607327bcc4957448da279b5 AS runtime
# 65532: distroless' nonroot user. Numeric, so runAsNonRoot can check it.
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/scaler"]

FROM runtime AS prebuilt
COPY out/scaler /scaler

# Default (last) stage: compiled above.
FROM runtime
COPY --from=build /scaler /scaler
