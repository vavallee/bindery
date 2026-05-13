FROM golang:1.25-alpine@sha256:8d22e29d960bc50cd025d93d5b7c7d220b1ee9aa7a239b3c8f55a57e987e8d45 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /discord-stats ./cmd/discord-stats

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /discord-stats /discord-stats
ENTRYPOINT ["/discord-stats"]
