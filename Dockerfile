FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o app .

FROM alpine:latest
RUN addgroup -S appgroup && adduser -S appuser -G appgroup
WORKDIR /app
COPY --from=builder /app/app .
RUN mkdir -p /app/cdn && chown -R appuser:appgroup /app
RUN chown appuser:appgroup app
USER appuser
VOLUME /app/cdn

ENTRYPOINT ["./app"]
