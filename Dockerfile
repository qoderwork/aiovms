# Multi-stage build
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /build

ENV GOPROXY=https://goproxy.cn,direct
ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /vms ./cmd/server

FROM alpine:3.24

RUN apk add --no-cache ca-certificates tzdata \
    && ln -sf /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
    && echo "Asia/Shanghai" > /etc/timezone

COPY --from=builder /vms /usr/local/bin/vms
COPY configs/config.yaml /etc/aiovms/config.yaml
EXPOSE 8081
ENTRYPOINT ["vms", "-config", "/etc/aiovms/config.yaml"]
