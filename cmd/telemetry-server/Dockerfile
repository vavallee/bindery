FROM golang:1.27-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /telemetry-server ./cmd/telemetry-server

# Distroless runtime — parity with the main Dockerfile. The binary is a static
# CGO-free build (modernc.org/sqlite is pure Go), so distroless/static is
# sufficient: no runtime sqlite package, no shell, minimal attack surface.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:1b7b9f0f0e0a1d2155f531db587cc48ec26aaf97ab64364225f5bf18a054e66a
COPY --from=builder /telemetry-server /telemetry-server
# Numeric non-root UID, matching runAsUser in deploy/telemetry-server.yaml.
USER 1000:1000
EXPOSE 8080
# No HEALTHCHECK: distroless has no shell/curl, and deploy/telemetry-server.yaml
# already defines a readinessProbe on /health.
ENTRYPOINT ["/telemetry-server"]
