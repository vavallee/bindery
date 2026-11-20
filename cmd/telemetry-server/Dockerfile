FROM golang:1.26.11-alpine@sha256:ae52b5bc6ed045a9ed26ab03dc19327a802a9cc9aa8b27ed57f5c6b60579b0bb AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# BUILD_SHA is stamped into the binary and reported on /api/stats, so the
# drift check can ask the running server which build it is. Defaults to
# "unknown" for a local build that does not pass it.
ARG BUILD_SHA=unknown
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X main.buildSHA=${BUILD_SHA}" \
      -o /telemetry-server ./cmd/telemetry-server

# Distroless runtime — parity with the main Dockerfile. The binary is a static
# CGO-free build (modernc.org/sqlite is pure Go), so distroless/static is
# sufficient: no runtime sqlite package, no shell, minimal attack surface.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931
COPY --from=builder /telemetry-server /telemetry-server
# Numeric non-root UID, matching runAsUser in the deployment (which lives in
# the homelab repo at kubernetes/apps/default/bindery-ping).
USER 1000:1000
EXPOSE 8080
# No HEALTHCHECK: distroless has no shell/curl, and the deployment already
# defines a readinessProbe on /health.
ENTRYPOINT ["/telemetry-server"]
