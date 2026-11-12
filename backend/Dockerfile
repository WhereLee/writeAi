FROM golang:1.26-alpine AS builder
RUN apk add --no-cache git
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o server ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
RUN addgroup -S app && adduser -S app -G app
COPY --from=builder --chown=app:app /app/server /app/server
USER app
EXPOSE 8080
ENTRYPOINT ["/app/server"]


