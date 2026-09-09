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
RUN ARIANG_VER=$(wget -qO- https://api.github.com/repos/mayswind/AriaNg/releases/latest | grep 'tag_name' | cut -d\" -f4) \
    && wget -O AriaNg.zip https://github.com/mayswind/AriaNg/releases/download/${ARIANG_VER}/AriaNg-${ARIANG_VER}-AllInOne.zip \
    && unzip AriaNg.zip -d /www \
    && curl -fsSL https://raw.githubusercontent.com/SuperNG6/docker-aria2/master/install.sh | bash

COPY web/inject.css /www/inject.css
COPY web/inject.js /www/inject.js

# 在 index.html 中注入样式与脚本引用
RUN sed -i 's@</head>@<link rel="stylesheet" href="inject.css"><script src="inject.js"></script></head>@' /www/index.html

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
    RUT=true

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
