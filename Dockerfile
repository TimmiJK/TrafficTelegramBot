FROM golang:1.25-alpine AS builder

WORKDIR /src

RUN apk add --no-cache tzdata

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/traffic-bot .

FROM alpine:3.20

RUN apk add --no-cache \
        ca-certificates \
        tzdata \
        sqlite \
        postgresql-client \
    && addgroup -S app \
    && adduser  -S app -G app \
    && mkdir -p /app/logs /app/data \
    && chown -R app:app /app

WORKDIR /app

COPY --from=builder /out/traffic-bot /app/traffic-bot

USER app

VOLUME ["/app/data", "/app/logs"]

ENTRYPOINT ["/app/traffic-bot"]