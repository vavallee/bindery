FROM golang:1.26-alpine@sha256:3889b425f035be855a72fb4755265311293b6d414521f0a519d819df32222d83 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /telemetry-server ./cmd/telemetry-server

# Distroless runtime — parity with the main Dockerfile. The binary is a static
# CGO-free build (modernc.org/sqlite is pure Go), so distroless/static is
# sufficient: no runtime sqlite package, no shell, minimal attack surface.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=builder /telemetry-server /telemetry-server
# Numeric non-root UID, matching runAsUser in deploy/telemetry-server.yaml.
USER 1000:1000
EXPOSE 8080
# No HEALTHCHECK: distroless has no shell/curl, and deploy/telemetry-server.yaml
# already defines a readinessProbe on /health.
ENTRYPOINT ["/telemetry-server"]
