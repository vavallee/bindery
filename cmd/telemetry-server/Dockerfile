FROM golang:1.26-alpine@sha256:3ad57304ad93bbec8548a0437ad9e06a455660655d9af011d58b993f6f615648 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /telemetry-server ./cmd/telemetry-server

# Distroless runtime — parity with the main Dockerfile. The binary is a static
# CGO-free build (modernc.org/sqlite is pure Go), so distroless/static is
# sufficient: no runtime sqlite package, no shell, minimal attack surface.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:d093aa3e30dbadd3efe1310db061a14da60299baff8450a17fe0ccc514a16639
COPY --from=builder /telemetry-server /telemetry-server
# Numeric non-root UID, matching runAsUser in deploy/telemetry-server.yaml.
USER 1000:1000
EXPOSE 8080
# No HEALTHCHECK: distroless has no shell/curl, and deploy/telemetry-server.yaml
# already defines a readinessProbe on /health.
ENTRYPOINT ["/telemetry-server"]
