FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY . .
RUN go mod tidy
RUN go build -o sizehunt cmd/server/main.go

FROM alpine:3.19
WORKDIR /app
COPY --from=builder /app/sizehunt .
CMD ["./sizehunt"]
