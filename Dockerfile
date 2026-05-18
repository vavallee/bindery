# Digest-pinned for supply-chain integrity. Dependabot keeps tags + digests
# in sync on a weekly cadence (see .github/dependabot.yml).

# Stage 1: Build frontend (runs on the builder's native arch — output is arch-agnostic JS)
FROM --platform=$BUILDPLATFORM node:26-alpine@sha256:e71ac5e964b9201072425d59d2e876359efa25dc96bb1768cb73295728d6e4ea AS frontend
WORKDIR /app/web
COPY web/package*.json ./
RUN npm ci
COPY web/ .
RUN npm run build

# Stage 2: Build Go binary (native on BUILDPLATFORM, cross-compile to TARGETOS/TARGETARCH)
FROM --platform=$BUILDPLATFORM golang:1.26.3-alpine@sha256:91eda9776261207ea25fd06b5b7fed8d397dd2c0a283e77f2ab6e91bfa71079d AS builder
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
FROM gcr.io/distroless/static-debian12:nonroot@sha256:d093aa3e30dbadd3efe1310db061a14da60299baff8450a17fe0ccc514a16639
COPY --from=builder /bindery /bindery
USER nonroot
EXPOSE 8787
# No shell in distroless, so invoke the binary directly. The healthcheck
# subcommand hits /api/v1/health on localhost and exits 0/1 accordingly.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD ["/bindery", "healthcheck"]
ENTRYPOINT ["/bindery"]
