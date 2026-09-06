# Digest-pinned for supply-chain integrity. Dependabot keeps tags + digests
# in sync on a weekly cadence (see .github/dependabot.yml).

# Stage 1: Build frontend (runs on the builder's native arch — output is arch-agnostic JS)
FROM --platform=$BUILDPLATFORM node:26-alpine@sha256:2d984a15c9b54fd0aeb608b8e0d0d83529eb34d2966db27a1fb4f1edc3d298a3 AS frontend
WORKDIR /app/web
COPY web/package*.json ./
RUN npm ci
COPY web/ .
# The web test suite asserts its Unicode fold against the same fixture the Go
# test uses (see docs/search-design.md): one corpus, so the key written into the
# database and the fold applied to the query cannot drift apart. `npm run build`
# type-checks the tests too, so the fixture has to exist here even though
# nothing at runtime reads it.
COPY internal/textutil/testdata/ /app/internal/textutil/testdata/
RUN npm run build

# Stage 2: Build Go binary (native on BUILDPLATFORM, cross-compile to TARGETOS/TARGETARCH)
FROM --platform=$BUILDPLATFORM golang:1.26.6-alpine@sha256:3889b425f035be855a72fb4755265311293b6d414521f0a519d819df32222d83 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /app/web/dist ./internal/webui/dist
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-w -s -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${BUILD_DATE}" \
    -o /bindery ./cmd/bindery

# Stage 3: Minimal runtime
FROM gcr.io/distroless/static-debian13:nonroot@sha256:1c2c046bc09ed40fad370b599a0b1ae7987f55b01e247cf27a7c27cd97e5bbc7
# OCI image metadata so registries and `docker inspect` surface the MIT license
# and source, matching the repo's LICENSE.
LABEL org.opencontainers.image.title="Bindery" \
      org.opencontainers.image.description="Automated book download manager for Usenet & Torrents" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.source="https://github.com/vavallee/bindery" \
      org.opencontainers.image.url="https://github.com/vavallee/bindery"
COPY --from=builder /bindery /bindery
# Attribution travels with the image: Bindery's own MIT license plus the
# licenses and NOTICE files of everything statically linked into the binary and
# embedded in the web bundle.
COPY LICENSE /LICENSE
COPY THIRD_PARTY_LICENSES.md /THIRD_PARTY_LICENSES.md
USER nonroot
EXPOSE 8787
# No shell in distroless, so invoke the binary directly. The healthcheck
# subcommand hits /api/v1/health on localhost and exits 0/1 accordingly.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD ["/bindery", "healthcheck"]
ENTRYPOINT ["/bindery"]
