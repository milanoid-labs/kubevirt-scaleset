# The scaler: a static Go binary on distroless (no shell, no package manager), non-root.
#
#   docker build .                   compiles in Docker (local use)
#   docker build --target prebuilt . packs a binary built beforehand at out/scaler (CI:
#                                    .github/workflows/scaler.yaml compiles outside Docker)

# The build stage runs on the build machine's own platform and cross-compiles (no emulation).
FROM --platform=$BUILDPLATFORM golang:1.27.4@sha256:205c7636d74e0ffbe3d007162df7d74f859f5a8c8322dfb9f907977686c126cf AS build
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

FROM gcr.io/distroless/static-debian13:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931 AS runtime
# 65532: distroless' nonroot user. Numeric, so runAsNonRoot can check it.
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/scaler"]

FROM runtime AS prebuilt
COPY out/scaler /scaler

# Default (last) stage: compiled above.
FROM runtime
COPY --from=build /scaler /scaler
