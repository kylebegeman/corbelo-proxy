FROM golang:1.24-alpine AS builder
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/corbelo-proxy ./cmd/corbelo-proxy

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/corbelo-proxy /corbelo-proxy
EXPOSE 8317
ENTRYPOINT ["/corbelo-proxy"]
