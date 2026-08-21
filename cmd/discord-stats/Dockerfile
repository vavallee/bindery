FROM golang:1.27rc3-alpine@sha256:c5aca77a4d16cb6688dbf3ccade67eff6f05ee208bc854d060e6947f5c27e23c AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /discord-stats ./cmd/discord-stats

FROM gcr.io/distroless/static-debian12:nonroot@sha256:1b7b9f0f0e0a1d2155f531db587cc48ec26aaf97ab64364225f5bf18a054e66a
COPY --from=builder /discord-stats /discord-stats
ENTRYPOINT ["/discord-stats"]
