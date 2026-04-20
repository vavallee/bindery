# Digest-pinned for supply-chain integrity. Dependabot keeps tags + digests
# in sync on a weekly cadence (see .github/dependabot.yml).

# Stage 1: Build frontend (runs on the builder's native arch — output is arch-agnostic JS)
FROM --platform=$BUILDPLATFORM node:25-alpine@sha256:ad82ecad30371c43f4057aaa4800a8ed88f9446553a2d21323710c7b937177fc AS frontend
WORKDIR /app/web
COPY web/package*.json ./
RUN npm ci
COPY web/ .
RUN npm run build

# Stage 2: Build Go binary (native on BUILDPLATFORM, cross-compile to TARGETOS/TARGETARCH)
FROM --platform=$BUILDPLATFORM golang:1.26.2-alpine@sha256:f85330846cde1e57ca9ec309382da3b8e6ae3ab943d2739500e08c86393a21b1 AS builder
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
FROM gcr.io/distroless/static-debian12:nonroot@sha256:a9329520abc449e3b14d5bc3a6ffae065bdde0f02667fa10880c49b35c109fd1
COPY --from=builder /bindery /bindery
USER nonroot
EXPOSE 8787
# No shell in distroless, so invoke the binary directly. The healthcheck
# subcommand hits /api/v1/health on localhost and exits 0/1 accordingly.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD ["/bindery", "healthcheck"]
ENTRYPOINT ["/bindery"]
