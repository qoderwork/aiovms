# Multi-stage build — final image < 20MB (no ffmpeg dependency)
FROM golang:1.23-alpine AS builder
RUN apk add --no-cache git
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /vms ./cmd/server

FROM alpine:3.24
ENV TZ=Asia/Shanghai
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /vms /usr/local/bin/vms
COPY configs/config.yaml /etc/aiovms/config.yaml
EXPOSE 8081
ENTRYPOINT ["vms", "-config", "/etc/aiovms/config.yaml"]
