# 阶段 1: 编译 Go aria2-controller
FROM golang:1.22-alpine AS go-builder
WORKDIR /build
COPY app/go.mod app/go.sum* ./
RUN go mod download
COPY app/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o aria2-controller .

# 阶段 2: 下载 static aria2c 与 AriaNg AllInOne 并注入前端增强文件
FROM alpine:3.20 AS web-builder
RUN apk add --no-cache curl wget unzip bash tar
WORKDIR /tmp

# AriaNg 版本从 releases/latest 的跳转地址解析，不调用（未认证时容易被限流的）GitHub API
RUN LATEST_URL=$(curl -fsSL -o /dev/null -w '%{url_effective}' https://github.com/mayswind/AriaNg/releases/latest) \
    && ARIANG_VER=${LATEST_URL##*/} \
    && [ -n "${ARIANG_VER}" ] && [ "${ARIANG_VER}" != "latest" ] \
    && echo "AriaNg version: ${ARIANG_VER}" \
    && curl -fsSL -o AriaNg.zip "https://github.com/mayswind/AriaNg/releases/download/${ARIANG_VER}/AriaNg-${ARIANG_VER}-AllInOne.zip" \
    && unzip -q AriaNg.zip -d /www \
    && test -f /www/index.html

# static aria2c（与 SuperNG6/docker-aria2 上游 Dockerfile 相同，取自 Aria2-Pro-Core 的 Release）
RUN MACHINE=$(uname -m) \
    && case "${MACHINE}" in \
         x86_64) ARCH=x86_64 ;; \
         aarch64) ARCH=arm64 ;; \
         armv7l|armv6l) ARCH=armhf ;; \
         i386|i686) ARCH=i386 ;; \
         *) echo "unsupported architecture: ${MACHINE}" >&2; exit 1 ;; \
       esac \
    && curl -fsSL -o /tmp/aria2.tar.gz "https://github.com/SuperNG6/Aria2-Pro-Core/releases/latest/download/aria2-static-linux-${ARCH}.tar.gz" \
    && tar -xzf /tmp/aria2.tar.gz -C /usr/local/bin \
    && test -x /usr/local/bin/aria2c \
    && /usr/local/bin/aria2c -v > /tmp/aria2c-version \
    && head -n 1 /tmp/aria2c-version \
    && rm -f /tmp/aria2.tar.gz /tmp/aria2c-version

COPY web/inject.css /www/inject.css
COPY web/inject.js /www/inject.js
COPY web/mirror.js /www/mirror.js

# 在 index.html 中注入样式与脚本引用
RUN sed -i 's@</head>@<link rel="stylesheet" href="inject.css"><script src="inject.js"></script><script src="mirror.js"></script></head>@' /www/index.html \
    && grep -q '<script src="mirror.js"></script></head>' /www/index.html

# 阶段 3: 最终运行镜像 (基于 superng6/alpine:3.22，包含 s6-overlay)
FROM superng6/alpine:3.22

LABEL maintainer="flintylemming"

ENV PORT=8080 \
    BTPORT=32516 \
    USERNAME=admin \
    PASSWORD=admin888 \
    PUID=1000 \
    PGID=1000 \
    TZ=Asia/Shanghai \
    CACHE=128M \
    UT=true \
    RUT=true \
    GITHUB_TOKEN=""

# 安装 Nginx、apache2-utils (htpasswd)、gettext (envsubst)、bash、curl、jq 等
RUN apk add --no-cache \
    nginx \
    apache2-utils \
    gettext \
    bash \
    curl \
    jq \
    findutils \
    shadow \
    && rm -rf /var/cache/apk/*

# 复制 root 目录与产物
COPY root/ /
COPY --from=go-builder /build/aria2-controller /app/aria2-controller
COPY --from=web-builder /www/ /www/
COPY --from=web-builder /usr/local/bin/aria2c /usr/local/bin/aria2c

RUN chmod a+x /usr/local/bin/aria2c /app/aria2-controller \
    && chmod a+x /aria2/script/*.sh \
    && chmod a+x /etc/cont-init.d/* \
    && chmod a+x /etc/services.d/*/run

VOLUME /config /downloads

EXPOSE 8080 32516 32516/udp
