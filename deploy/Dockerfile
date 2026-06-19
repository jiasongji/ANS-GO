# syntax=docker/dockerfile:1
# =============================================================================
# ANS-GO 面板多阶段构建（推 ghcr.io/jiasongji/ansgo-panel）
# 构建上下文 = deploy/ 目录：
#   docker buildx build --platform linux/amd64 -t ghcr.io/jiasongji/ansgo-panel:latest -f Dockerfile . --push
#
# 说明：面板需 exec 宿主的 ansgo-admin / systemctl / journalctl 管理服务，
# 因此容器化主要用于「可复现构建产物」与「只读配置/查看」场景。
# 完整服务管理能力（重启/改端口）建议裸金属部署（LXC 低配亦推荐裸金属）。
# =============================================================================
FROM golang:1.26-alpine AS build
WORKDIR /src
# 先拷依赖利用缓存
COPY panel/go.mod panel/go.sum ./
RUN go mod download
COPY panel/ .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags "-s -w" -o /out/ansgo-panel .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 1000 ansgo
COPY --from=build /out/ansgo-panel /usr/local/bin/ansgo-panel
USER root
EXPOSE 15608
VOLUME ["/etc/ansgo", "/etc/ssl/ansgo"]
ENTRYPOINT ["/usr/local/bin/ansgo-panel"]
