FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY logger/go.mod logger/go.sum ./
RUN go mod download
COPY logger/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -o radio-playlog .

FROM alpine:3.19
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /app/radio-playlog .
COPY logger/configuration.gcp.json configuration.json
EXPOSE 8080
CMD ["./radio-playlog"]
