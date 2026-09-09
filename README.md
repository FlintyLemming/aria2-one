# 7815-aria2: 单端口 Aria2 控制台服务

真正的单端口纯粹下载控制台容器镜像。仅暴露唯一的 Web 端口（默认 8080），内置前置 Basic Auth 统一鉴权、同源自适应零配置连接、直链本地下载与流媒体在线预览，并通过内部 Go 中间件实现删除任务时自动彻底清理磁盘关联文件。

## 核心特性

1. **真正单端口暴露**：对外仅需开放 8080 端口，前端 AriaNg、下载直链与内部 RPC 全由前置 Nginx 智能分流。内部关闭 6800 端口暴露，安全清爽。
2. **同源零配置自适应**：前端自适应识别当前访问的 Hostname/Port/Protocol 直连 `/jsonrpc`，换 IP、换域名、反代均 100% 自动成功连接。
3. **下载直链与媒体预览**：已完成任务一键拉回本地电脑，多文件任务支持逐个下载并支持音视频/图片直接在线流式播放预览。
4. **一致性物理删除**：通过内部 `aria2-controller` 透明拦截删除指令，移除任务时自动物理删除对应磁盘文件与伴生 `.aria2` 文件，并递归清理空目录。
5. **BT Tracker 自动更新**：容器启动自动拉取最新优质 Trackers，支持每日凌晨通过 RPC 无感热重载。
6. **PUID/PGID 权限支持**：灵活映射宿主机权限，杜绝权限冲突。

## 快速使用

### Docker Compose (推荐)

```yaml
services:
  aria2:
    image: flintylemming/7815-aria2:latest
    container_name: 7815-aria2
    restart: unless-stopped
    ports:
      - "8080:8080"
      - "32516:32516"
      - "32516:32516/udp"
    environment:
      - USERNAME=admin
      - PASSWORD=admin888
      - PUID=1000
      - PGID=1000
      - TZ=Asia/Shanghai
    volumes:
      - ./config:/config
      - ./downloads:/downloads
```

打开浏览器访问 `http://<服务器IP>:8080`，输入设定的账号密码即可畅快使用。

## 环境变量说明

| 变量名 | 默认值 | 作用描述 |
| :--- | :--- | :--- |
| `PORT` | `8080` | 控制台对外访问端口 |
| `BTPORT` | `32516` | BT/DHT 监听端口 (TCP/UDP) |
| `USERNAME` | `admin` | 控制台登录用户名 (留空关闭鉴权) |
| `PASSWORD` | `admin888` | 控制台登录密码 |
| `PUID` | `1000` | 运行用户的 UID |
| `PGID` | `1000` | 运行用户的 GID |
| `TZ` | `Asia/Shanghai` | 时区 |
| `CACHE` | `128M` | Aria2 写入缓存 |
| `UT` | `true` | 启动时是否自动拉取最新 Trackers |
| `RUT` | `true` | 是否开启每日凌晨定时热更新 Trackers |
