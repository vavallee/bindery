# Digest-pinned for supply-chain integrity. Dependabot keeps tags + digests
# in sync on a weekly cadence (see .github/dependabot.yml).

# Stage 1: Build frontend (runs on the builder's native arch — output is arch-agnostic JS)
FROM --platform=$BUILDPLATFORM node:26-alpine@sha256:aadf416b2cdce311a8811ba3f0608a61b77dbf997500e2eafe781b51f6a0b019 AS frontend
WORKDIR /app/web
COPY web/package*.json ./
RUN npm ci
COPY web/ .
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
FROM gcr.io/distroless/static-debian12:nonroot@sha256:1b7b9f0f0e0a1d2155f531db587cc48ec26aaf97ab64364225f5bf18a054e66a
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
