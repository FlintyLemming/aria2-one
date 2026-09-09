# 单端口 Aria2 控制台服务架构与设计规范 (Design Spec)

## 1. 概述与背景

本项目旨在参考 [`docker-aria2`](file:///home/flintylemming/Projects/docker-aria2) 的优秀特性（Tracker 自动更新、高并发下载核心、PUID/PGID 权限映射），构建一个真正的**单端口纯粹下载控制台**容器服务。

### 核心痛点与解决方案
1. **割裂的文件下载与管理流程**：原项目及常规 Aria2 镜像中，AriaNg 仅负责向服务器提交下载任务，下载完成后用户必须额外借助第三方文件管理器（如 FileBrowser、Index 目录服务）或进入容器终端才能将文件拉回本地电脑。
   * **解决方案**：在 AriaNg 中直接集成“下载到本地”能力。单文件任务直接在列表点击下载；多文件任务在文件列表中逐个下载，并支持流媒体预览。完全淘汰额外的文件管理器进程。
2. **任务删除与磁盘文件不一致**：Aria2 原生 RPC 的 `aria2.removeDownloadResult` 仅从内存中清除任务记录，绝不删除磁盘文件。如果用户在 Web 页面删除任务，物理文件会变成只有进入容器才能删除的磁盘孤儿。
   * **解决方案**：引入内部智能控制器（`aria2-controller`）作为 RPC 中间件，透明拦截删除指令。在移除任务的同时，自动查询关联的所有文件路径并彻底在磁盘上执行物理删除与空目录清理。
3. **RPC 连接繁琐与多端口暴露**：原项目暴露多个端口（6800 RPC、8080 WebUI），且 AriaNg 默认配置依赖本地 `localStorage`，更换访问 IP/域名、使用无痕模式或手机打开时频繁报“未连接”。
   * **解决方案**：单端口化。对外仅暴露唯一的 Web 端口（默认 8080）。前端自动读取当前访问环境（Hostname、Port、Protocol）实现零配置同源直连；内部关闭 6800 对外暴露，隐藏冗余 RPC 设置。
4. **统一身份认证**：无需复杂的外部多用户系统，前置网关提供统一的 HTTP Basic Auth（用户名/密码），一次登录全局生效。

---

## 2. 总体架构与系统拓扑

```
[ 用户浏览器 / 客户端 ]
        │
        │ HTTP / WebSocket (仅单一 Web 端口: 8080, Basic Auth 保护)
        ▼
┌─────────────────────────────────────────────────────────────┐
│ 容器边界 (7815-aria2)                                        │
│                                                             │
│   ┌─────────────────────────────────────────────────────┐   │
│   │ Nginx 前置统一网关 (:8080)                          │   │
│   │  ├── 全局 Basic Auth 统一鉴权                       │   │
│   │  ├── GET /         -> 增强型 AriaNg 静态资源         │   │
│   │  ├── ANY /jsonrpc  -> 反代至 127.0.0.1:8081 (Go 代理) │   │
│   │  └── GET /download/-> 映射 /downloads (Range 断点续传)│   │
│   └────────────────────────┬────────────────────────────┘   │
│                            │                                │
│                            ▼                                │
│   ┌─────────────────────────────────────────────────────┐   │
│   │ aria2-controller 内部智能代理 (:8081)               │   │
│   │  ├── 拦截 aria2.remove / removeDownloadResult       │   │
│   │  ├── 查询任务路径 -> 物理删除 /downloads 关联文件   │   │
│   │  └── 转发其余 RPC 请求至 aria2c                    │   │
│   └────────────────────────┬────────────────────────────┘   │
│                            │                                │
│                            ▼                                │
│   ┌─────────────────────────────────────────────────────┐   │
│   │ aria2c 高性能内核 (:6800, 仅监听 127.0.0.1)          │   │
│   │  ├── 支持 1024 线程高并发下载                       │   │
│   │  ├── 监听 BT 端口 (:32516 TCP/UDP, P2P 流量)        │   │
│   │  └── 触发 clean.sh 清理残余 .aria2 文件             │   │
│   └─────────────────────────────────────────────────────┘   │
│                                                             │
│   ┌─────────────────────────────────────────────────────┐   │
│   │ 自动化任务 (Crond / s6)                              │   │
│   │  └── tracker.sh: 启动时 & 每日定时更新 400+ Trackers│   │
│   └─────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
```

---

## 3. 详细模块设计

### 3.1 前端增强模块 (AriaNg Enhanced)
基于 AriaNg 官方最新 AllInOne 单文件发布包，在 Docker 构建阶段注入 `web/inject.js` 和 `web/inject.css`：

1. **同源环境自适应与自动连接 (Auto-Connect)**
   * 在 AngularJS 启动（`angular.bootstrap` 或 DOM 加载）前运行环境嗅探：
     ```javascript
     const host = window.location.hostname;
     const port = window.location.port || (window.location.protocol === 'https:' ? '443' : '80');
     const protocol = window.location.protocol === 'https:' ? 'wss' : 'ws';
     const httpProtocol = window.location.protocol === 'https:' ? 'https' : 'http';
     ```
   * 自动设置 AriaNg 的活动 RPC 配置，指向同源 `/jsonrpc`，无论用户通过内网 IP、公网域名、端口映射还是反向代理打开，均能 100% 自动成功连接。
   * 隐藏侧边栏与设置中心的“Aria2 RPC 设置”菜单，避免用户误改内部配置。

2. **直链本地下载功能注入 (Direct Local Download)**
   * **已完成任务列表**：
     * 针对单文件下载任务（文件数为 1）：在任务项操作区自动注入 `[ ⬇️ 下载到本地 ]` 按钮，点击直接请求 `/download/<相对路径>`，触发浏览器原生另存为。
     * 针对多文件任务：提供快捷入口直达任务详情的“文件列表”页面。
   * **任务详情 -> 文件列表 (Files)**：
     * 为每个已下载完毕的文件项注入独立下载按钮：
       `<a href="/download/{fileRelativePath}" download="{fileName}" class="btn btn-xs btn-primary">下载</a>`
     * 识别音视频（mp4/mkv/webm/mp3）、图片（jpg/png/gif）及文档格式，增加直接在新标签页在线流式预览/播放按钮。

3. **任务与文件一致性删除文案引导**
   * 修改任务删除确认弹窗文案，明确提示用户：“**此操作将从下载列表移除任务，并永久删除磁盘上的对应物理文件！**”，使用户的心理模型与后台真实操作完全一致。

---

### 3.2 内部智能代理模块 (aria2-controller)
使用 Go 编写单文件轻量服务，在多阶段构建中编译为仅 ~5MB 的纯静态二进制，监听内部 `127.0.0.1:8081`。

1. **JSON-RPC 智能拦截机制**：
   * 接收到来自 Nginx 的 `/jsonrpc` 请求后，解析 JSON 结构体中的 `method`：
   * 若 `method` 属于 `aria2.remove` 或 `aria2.removeDownloadResult`（或批量调用的 `system.multicall`）：
     1. **预查文件路径**：提取任务 `gid`，在移除前向本地 aria2c（`127.0.0.1:6800`）发送 `aria2.tellStatus(gid)` 查询任务详情。
     2. **解析目标路径**：从返回结果中提取任务主目录 `dir`、全部具体文件 `files[*].path`。
     3. **注销任务记录**：将删除指令转发给 aria2c 执行注销。
     4. **物理清理磁盘**：
        * 逐一删除 `files[*].path`。
        * 检查并删除控制文件（如 `${path}.aria2`）。
        * 若任务处于多层级文件夹中，自底向上检查并清理因删除该任务而产生的空文件夹，避免磁盘垃圾残留。
   * 其余所有 RPC 指令（添加下载、查询状态、暂停、恢复、WebSocket 长连接）直接透明反向代理至 `127.0.0.1:6800`。

2. **安全防护机制**：
   * 严格校验待删除路径必须位于 `/downloads` 目录内，杜绝通过相对路径逃逸（Path Traversal）误删容器系统文件的风险。

---

### 3.3 网关服务模块 (Nginx)
作为单一公开的 HTTP/WebSocket 入口，监听 `${PORT}`（默认 `8080`）：

1. **统一账号密码鉴权**：
   * 由初始化脚本 `20-auth` 根据环境变量 `USERNAME` 和 `PASSWORD` 自动生成 `/etc/nginx/.htpasswd`。
   * 若未设置密码或设为空，则自动关闭 `auth_basic`。
   * 所有路由（`/`、`/jsonrpc`、`/download/`）统一受此保护，浏览器输入一次即可全站共享凭据。

2. **大文件直链下载优化**：
   ```nginx
   location /download/ {
       alias /downloads/;
       sendfile on;
       sendfile_max_chunk 1m;
       tcp_nopush on;
       tcp_nodelay on;
       autoindex off;
       # 确保支持 HTTP Range (断点续传)
       add_header Accept-Ranges bytes;
   }
   ```

3. **RPC 通信反向代理**：
   ```nginx
   location /jsonrpc {
       proxy_pass http://127.0.0.1:8081/jsonrpc;
       proxy_http_version 1.1;
       proxy_set_header Upgrade $http_upgrade;
       proxy_set_header Connection "upgrade";
       proxy_set_header Host $host;
       proxy_read_timeout 86400s;
   }
   ```

---

### 3.4 下载核心与后台任务 (aria2c & s6-overlay)
1. **多架构静态编译核心**：采用 P3TERX 静态编译版 `aria2c` 1.37.0，解除并发与连接数限制。
2. **PUID / PGID 权限控制**：s6 初始化脚本根据用户指定的 UID/GID 自动调整 `/downloads` 与 `/config` 的属主，确保 aria2c 与 controller 进程具备完整的删除与读写权限。
3. **Tracker 自动化更新**：
   * 容器启动时运行 `tracker.sh`，从 Github 获取最新 Trackers 并注入配置。
   * 若 `RUT=true`，配置 crond 每日凌晨自动拉取最新 Trackers 并通过 RPC `aria2.changeGlobalOption` 热重载，无需重启容器。
4. **下载完成清理**：配置 `on-download-complete=/aria2/script/clean.sh`，确保下载完毕后及时清理 `.aria2` 临时文件。

---

## 4. 环境变量与配置定义

| 变量名 | 默认值 | 必填 | 作用描述 |
| :--- | :--- | :---: | :--- |
| `PORT` | `8080` | 否 | Web 控制台对外的唯一 HTTP 访问端口 |
| `BTPORT` | `32516` | 否 | BT/DHT 网络监听端口 (TCP/UDP) |
| `USERNAME` | `admin` | 否 | 控制台登录用户名（留空则不开启鉴权） |
| `PASSWORD` | `admin888` | 否 | 控制台登录密码 |
| `PUID` | `1000` | 否 | 运行用户 UID |
| `PGID` | `1000` | 否 | 运行用户 GID |
| `TZ` | `Asia/Shanghai` | 否 | 容器时区 |
| `CACHE` | `128M` | 否 | 磁盘写入缓存 |
| `UT` | `true` | 否 | 容器启动时是否自动更新 Trackers |
| `RUT` | `true` | 否 | 是否启用每日定时 RPC 热更新 Trackers |

---

## 5. 项目工程结构

```
/home/flintylemming/Projects/7815-aria2/
├── Dockerfile                  # 多阶段构建文件
├── docker-compose.yml          # 开箱即用 Compose 范例
├── README.md                   # 项目使用与配置文档
├── .dockerignore
├── app/                        # 内部 RPC 智能中间件源码
│   ├── go.mod
│   └── main.go                 # 代理转发、路径解析、物理删除与安全校验
├── web/                        # 前端增强代码
│   ├── inject.js               # 零配置连接劫持、下载按钮渲染、删除语义提示
│   └── inject.css              # 样式微调
└── root/                       # 容器内文件层
    ├── aria2/
    │   ├── conf/
    │   │   ├── aria2.conf.default
    │   │   └── rpc-tracker
    │   └── script/
    │       ├── clean.sh
    │       └── tracker.sh
    └── etc/
        ├── cont-init.d/
        │   ├── 10-env
        │   ├── 20-auth
        │   ├── 30-aria2
        │   └── 40-tracker
        ├── nginx/
        │   └── nginx.conf.template
        └── services.d/
            ├── aria2/run
            ├── controller/run
            └── nginx/run
```

---

## 6. 测试与验证方案

1. **构建测试**：运行 `docker build -t test-aria2 .`，验证多阶段编译（Go 二进制、AriaNg 增强脚本注入）均正常完成。
2. **自适应连接测试**：启动容器，使用浏览器打开不同地址（如 `http://localhost:8080`、`http://127.0.0.1:8080`），验证控制台均能瞬间连上后端，无需手动配置 RPC。
3. **下载任务与直链拉取测试**：
   * 提交单个测试文件下载任务，下载完成后，验证任务卡片右侧直接展示 `[ ⬇️ 下载到本地 ]`，点击浏览器可直接以极速将文件存回本地。
   * 提交多文件 BT 任务，进入“文件列表”，验证每一行均有独立下载按钮，音视频支持在线流式播放。
4. **一致性删除测试（重点验证）**：
   * 在控制台列表中删除已完成的任务，检查容器内 `/downloads` 目录，确认对应的物理文件与相关空目录均被彻底删除，无任何孤儿文件遗留。
5. **身份认证测试**：测试配置 `USERNAME`/`PASSWORD` 后，未认证请求被 401 拦截，认证成功后页面及 `/download/` 文件拉取均畅通。
