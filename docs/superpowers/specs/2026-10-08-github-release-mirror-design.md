# GitHub Release 定期镜像 设计规范 (Design Spec)

## 1. 背景与目标

`scripts-public/github-release-mirror` 是一个独立 Python 脚本：读取 `config.yaml` 中的 `track-list`，对每个仓库请求 `releases/latest`，若本地不存在 `<saved-path>/<folder-name>/<tag>` 目录，则下载（可按后缀 / 正则过滤的）Release 资产并写出 `CHANGELOG.md`。依赖外部 cron 定时执行。

本特性把它变成 aria2-one 面板的内置功能：

1. 在 AriaNg 侧边栏新增 **「Release 镜像」** 页面（`#!/mirrors`），可视化地添加 / 编辑 / 启停 / 删除镜像项，查看最新版本、同步状态、已镜像的版本与文件（附直链）。
2. 定时检查由 `aria2-controller` 进程内的调度器完成，无需 cron。
3. 资产下载**交给 aria2 本身**（`aria2.addUri`），因此镜像下载同样享受多线程、断点续传，并且在 AriaNg 的任务列表里可见进度。
4. 一键导入原脚本的 `config.yaml`。

非目标：私有仓库资产下载（Token 仅用于提高 API 限额）；Release 草稿；多版本回溯下载（只镜像"最新"一个 Release，旧版本只在曾经同步过时才存在于磁盘）。

---

## 2. 架构

```
浏览器 (AriaNg + inject.js 的 #!/mirrors 页面)
   │  fetch /api/*  (Basic Auth 由 Nginx 统一校验)
   ▼
Nginx :8080  ── location /api/ ──►  aria2-controller :8081
                                      ├── MirrorStore     /config/github-mirror.json（配置 + 状态，原子写）
                                      ├── GitHubClient    GET {GITHUB_API_URL}/repos/{repo}/releases[/latest]
                                      ├── Aria2Client     aria2.addUri / tellActive / tellWaiting  → 127.0.0.1:6800
                                      ├── Scheduler       每 30s tick，按间隔触发同步 + 刷新下载进度
                                      └── ControllerProxy 删除拦截器：镜像目录内的文件受保护，不随任务删除
```

磁盘布局（与原脚本一致，便于直接复用旧目录）：

```
{downloadDir}/{settings.rootDir}/{mirror.folder}/{tag}/
    CHANGELOG.md          (saveChangelog=true 且 body 非空时写出；已存在则不覆盖)
    <asset 1>
    <asset 2>
```

`downloadDir` 默认 `/downloads`，`rootDir` 默认 `github-mirror` → 例如 `/downloads/github-mirror/PowerToys/v0.85.1/PowerToysSetup-0.85.1-x64.exe`，直链为 `/download/github-mirror/PowerToys/v0.85.1/PowerToysSetup-0.85.1-x64.exe`。

---

## 3. 后端（Go，`app/` 包 `main`）

### 3.1 启动参数 / 环境变量（`main.go`）

| flag | env | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-config-dir` | `CONFIG_DIR` | `/config` | 存放 `github-mirror.json` |
| `-github-api` | `GITHUB_API_URL` | `https://api.github.com` | GitHub API 基址（测试时可指向 mock） |
| — | `GITHUB_TOKEN` | 空 | 设置页未填写 Token 时的后备 Token |

`s6` 服务脚本 `root/etc/services.d/controller/run` 追加 `-config-dir=/config`。

### 3.2 数据模型与持久化（`<configDir>/github-mirror.json`）

```json
{
  "settings": {
    "rootDir": "github-mirror",
    "intervalMinutes": 360,
    "proxy": "",
    "githubToken": ""
  },
  "mirrors": [
    {
      "id": "9f2c1a7b3d4e5f60",
      "name": "PowerToys",
      "repo": "microsoft/PowerToys",
      "folder": "PowerToys",
      "enabled": true,
      "saveChangelog": true,
      "fileTypes": ["exe", "msi"],
      "regex": "",
      "includePrerelease": false,
      "keepVersions": 0,
      "createdAt": "2026-10-08T10:00:00Z"
    }
  ],
  "state": {
    "9f2c1a7b3d4e5f60": {
      "status": "synced",
      "latestTag": "v0.85.1",
      "releaseName": "Release v0.85.1",
      "releaseUrl": "https://github.com/microsoft/PowerToys/releases/tag/v0.85.1",
      "publishedAt": "2026-10-01T12:00:00Z",
      "lastCheck": "2026-10-08T10:00:05Z",
      "lastSuccess": "2026-10-08T10:00:05Z",
      "lastError": "",
      "files": [
        {"name": "PowerToysSetup-0.85.1-x64.exe", "size": 123456, "status": "done", "completedLength": 123456}
      ]
    }
  }
}
```

- 文件不存在时使用默认设置、空列表。写入采用 "写临时文件 + rename" 原子替换，权限 0600（含 Token）。所有读写受 `sync.Mutex` 保护。
- `id`：16 位随机 hex（`crypto/rand`）。
- 时间字段：`time.Time`，零值序列化为空字符串更友好——实现上用 `*time.Time` 或 `omitempty` 字符串均可，但 **API 输出中从未发生的时间必须是 `""` 或缺省，前端两者都要兼容**。

**MirrorState.status 取值**：

| 值 | 含义 |
| --- | --- |
| `idle` | 从未检查过 |
| `checking` | 正在请求 GitHub API / 投递任务 |
| `downloading` | 有文件仍在 aria2 中下载（active/waiting/paused） |
| `synced` | 最新 Release 的所有匹配文件已完整存在于磁盘 |
| `incomplete` | 没有文件在下载，但仍有文件缺失/不完整（aria2 任务失败等），下次检查会自动重试 |
| `error` | 最近一次检查失败，`lastError` 为原因（如 API 限流、仓库不存在、正则无效） |

**file.status 取值**：`done`（文件存在、无 `.aria2` 控制文件、大小与资产 size 一致）、`downloading`（在 aria2 active/waiting/paused 列表中，`completedLength` 来自 aria2）、`pending`（既不完整也不在下载中）。

**file.error**（字符串，可缺省/空）：文件为 `pending` 时，若 aria2 的已停止任务（`aria2.tellStopped`）中该路径**最近一次**任务状态为 `error`，填入其 `errorMessage`（为空时用 `errorCode`）；文件 `done`/`downloading` 时必须为空。镜像为 `incomplete` 且存在带 `error` 的文件时，`lastError` = `部分文件下载失败：<name>：<error>；<name>：<error>`。

### 3.3 校验与规范化

- `repo`：接受 `owner/name`，也接受 `https://github.com/owner/name`、带 `.git` 后缀或后续路径（如 `/releases`）的 URL，统一规范化为 `owner/name`；最终须匹配 `^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`。
- `name`：去首尾空格；为空时取仓库名部分。
- `folder`：去首尾空格；为空时取仓库名部分；不得包含 `/`、`\`、控制字符，不得为 `.` / `..`，长度 ≤ 128；**在所有镜像中唯一**（大小写不敏感），否则 400。
- `fileTypes`：逐项去空格、去前导 `.`、丢弃空项、去重；匹配时大小写不敏感地判断 `strings.HasSuffix(lower(asset), "."+lower(ext))`（也允许用户写 `tar.gz`）。
- `regex`：Go RE2 语法，**部分匹配**（`regexp.MatchString`）；必须能编译，否则 400。
- 过滤规则：`fileTypes` 非空时须命中其一；`regex` 非空时须匹配；两者都空则下载全部资产。
- `keepVersions`：≥ 0；0 表示保留全部版本。
- 设置 `rootDir`：去首尾空格与首尾 `/`；必须是相对路径，`filepath.Clean` 后不得为 `.`、不得以 `..` 开头、不得含 `..` 段；允许多级（`mirrors/github`）。默认 `github-mirror`。
- 设置 `intervalMinutes`：`0`（仅手动）或 `10 ≤ n ≤ 43200`。
- 设置 `proxy`：空，或 `http://[user:pass@]host[:port]`（aria2 的 `all-proxy` 只支持 HTTP 代理，故仅允许 `http://` 协议）。
- tag 与资产文件名落盘前做安全化：`/`、`\` 替换为 `_`，结果为空、`.`、`..` 时视为错误跳过；最终路径必须通过已有的 `IsSafeSubpath(downloadDir, p)` 校验。

### 3.4 同步算法（`Manager.Sync(id)`）

同一镜像同一时刻只允许一个同步在跑（`syncing map[string]bool`），重复触发直接忽略。

1. 状态置 `checking`，`lastCheck = now`。
2. 拉取 Release：
   - `includePrerelease=false`：`GET {api}/repos/{repo}/releases/latest`
   - `includePrerelease=true`：`GET {api}/repos/{repo}/releases?per_page=10`，取第一个 `draft=false` 的
   - 请求头：`Accept: application/vnd.github+json`、`X-GitHub-Api-Version: 2022-11-28`、`User-Agent: aria2-one`，有 Token 时 `Authorization: Bearer <token>`（设置中的 Token 优先，否则 `GITHUB_TOKEN` 环境变量）。
   - 使用设置中的 `proxy`（为空则直连，**不**读取环境代理以免行为不可预期），超时 30s。
   - 错误信息需可读：404 →「仓库不存在或没有正式 Release」；403/429 且 `X-RateLimit-Remaining: 0` →「GitHub API 限流，将于 HH:MM 重置，建议在设置中填写 Token」；其它非 2xx → 带状态码和 GitHub 返回的 `message`。
3. 过滤资产，计算 `versionDir = {downloadDir}/{rootDir}/{folder}/{safeTag}`；在 store 锁内**再次确认镜像仍存在**（已被删除则中止、不产生任何文件/aria2 副作用），然后 `MkdirAll(0755)`。资产下载 URL 必须是 `http`/`https`；当 GitHub API 基址为默认的 `https://api.github.com` 时，下载 URL 的主机还必须是 `github.com`（防御被篡改的元数据把 aria2 引向内网）。
4. `saveChangelog && body != ""` 且 `CHANGELOG.md` 不存在、且过滤后的资产中**没有**同名 `CHANGELOG.md` → 写出 body。
5. 通过 aria2 RPC 获取进行中任务：`aria2.tellActive(["gid","status","files","completedLength","totalLength"])` + `aria2.tellWaiting(0, 1000, 同上)`，建立 `path → completedLength` 映射（paused 在 waiting 列表中）。
6. 逐个资产：`done` → 跳过；在进行中映射里 → `downloading`；否则 `aria2.addUri([browser_download_url], {dir: versionDir, out: assetName, "allow-overwrite": "true", "auto-file-renaming": "false", "continue": "false", "follow-metalink": "false", "follow-torrent": "false", 有代理时 "all-proxy": proxy})`，记为 `downloading`（`completedLength=0`）。`addUri` 失败 → 该文件 `pending`，并把错误写入 `lastError`（状态最终为 `error`）。
   - `allow-overwrite=true` + `auto-file-renaming=false`：避免 aria2 在文件已存在（如资产被重新上传、大小变了）时生成 `xxx.1.exe`。
   - `continue=false`：**必须**。容器的全局 `aria2.conf` 设置了 `continue=true`，会把已存在、无控制文件的旧文件当作"已下载前缀"续传，导致重新上传的同名资产变成"旧前缀 + 新尾部"且仍被判定完整。设为 false 后有 `.aria2` 控制文件的中断下载仍会正常续传，无控制文件的旧文件会被截断重下。
   - `follow-metalink=false` / `follow-torrent=false`：`.meta4`/`.metalink`/`.torrent` 资产只作为普通文件保存，aria2 不得解析其内容去下载其它 URL（否则可被用作 SSRF，访问内网或本机无密钥的 aria2 RPC）。
7. 写入 `latestTag / releaseName / releaseUrl / publishedAt / files`；全部 `done` → `synced` 且 `lastSuccess = now`；有 `downloading` → `downloading`；否则 `incomplete`。`lastError` 清空（除非第 6 步有失败）。
8. 版本保留：**仅当当前版本已完整同步（状态变为 `synced`）时**才执行——无论是在本次 Sync 中直接 synced，还是之后由 `RefreshProgress` 把状态推进到 synced。`keepVersions > 0` 时，列出 `{rootDir}/{folder}/` 下的子目录，排除当前 `safeTag`，按目录 mtime 降序，保留前 `keepVersions-1` 个，其余 `os.RemoveAll`；跳过任何仍有文件在 aria2 进行中的目录；镜像已被删除时不执行；删除前再次校验路径严格位于 `{rootDir}/{folder}/` 下一层。
9. 任一步骤失败：`status=error`，`lastError=原因`，持久化。

**进度刷新（`Manager.RefreshProgress()`）**：对所有状态为 `downloading` 的镜像，不访问 GitHub，只基于 state 中的 `files` 重新判定每个文件的 `done/downloading/pending`（磁盘 + 一次 tellActive/tellWaiting），并据此更新镜像状态（全部 done → `synced` 并设置 `lastSuccess`，随后执行第 8 步版本保留；无下载中且有缺失 → `incomplete`，并按 `file.error` 规则通过一次 `aria2.tellStopped` 补全失败原因）。在调度器每个 tick 和 `GET /api/mirrors` 时调用（GET 中失败不影响返回）。Sync 第 7 步得出 `incomplete` 时同样补全失败原因。

**调度器**：启动后延迟 20s 开始，每 30s tick：先 `RefreshProgress()`，再对每个 `enabled` 镜像，若 `intervalMinutes > 0` 且（`lastCheck` 为空或 `now - lastCheck ≥ interval`）则顺序执行 `Sync`。通过 `context` 支持停止（测试用）。

### 3.5 删除拦截保护（`proxy.go`）

`ControllerProxy` 新增字段 `Protect func(path string) bool`。在 HTTP 与 WebSocket 两条删除路径中，调用 `RemoveTaskFiles` 前先过滤掉 `Protect(path) == true` 的路径（打印 `[cleaner] skip protected mirror file: ...`）。`main.go` 中注入 `manager.IsManagedPath`：判断路径是否位于 `{downloadDir}/{当前 rootDir}/` 之下。

原因：用户在 AriaNg「已完成」列表里删除一条镜像下载任务时，不应把镜像文件物理删除；镜像文件只通过镜像页面管理（保留策略 / 删除镜像时勾选删除文件）。

### 3.6 HTTP API（`mirror_api.go`，挂载在同一个 `http.ServeMux`，使用 Go 1.22 方法路由）

通用约定：
- 响应均为 `application/json; charset=utf-8`；错误统一 `{"error": "中文可读信息"}` 及合适的状态码（400 校验失败、404 不存在、409 冲突、415 Content-Type 错误、500 内部错误）。
- **CSRF 防护**：所有 `POST` / `PUT` 请求必须带 `Content-Type: application/json`（允许 `; charset=...`），否则 415。浏览器表单无法跨站发送该类型，跨域 fetch 会触发预检而被拒绝。
- 请求体上限 1 MiB；未知字段忽略。
- **永远不在响应中返回 Token 明文**，只返回 `githubTokenSet: bool`。

| 方法 & 路径 | 请求体 | 响应 |
| --- | --- | --- |
| `GET /api/mirrors` | — | `200 {"settings": SettingsView, "downloadDir": "/downloads", "mirrors": [MirrorView]}` |
| `POST /api/mirrors` | `MirrorInput` | `201 MirrorView`；`enabled` 时立即异步触发一次同步 |
| `PUT /api/mirrors/{id}` | `MirrorInput` | `200 MirrorView`（不移动已有文件；从禁用变为启用时异步触发同步） |
| `DELETE /api/mirrors/{id}?deleteFiles=true` | — | `200 {"ok": true}`。顺序：① 先从配置/状态中移除该镜像（之后的 Sync/Refresh 视为不存在）；② 若该镜像正在同步，取消其 context 并等待其结束；③ 对 aria2 中 active/waiting/paused 且文件路径位于 `{rootDir}/{folder}/` 下的任务调用 `aria2.forceRemove`（直接走 aria2，不经删除拦截器）；④ `deleteFiles=true` 时 `RemoveAll({rootDir}/{folder})`（路径安全校验） |
| `POST /api/mirrors/{id}/sync` | `{}` | `202 {"ok": true}`（异步；正在同步则忽略） |
| `POST /api/mirrors/sync` | `{}` | `202 {"ok": true}`（异步顺序同步所有 enabled 镜像） |
| `GET /api/mirrors/{id}/versions` | — | `200 {"versions": [VersionView]}` |
| `GET /api/mirror-settings` | — | `200 SettingsView` |
| `PUT /api/mirror-settings` | `SettingsInput` | `200 SettingsView` |
| `POST /api/mirrors/import` | `{"yaml": "<原脚本 config.yaml 文本>"}` | `200 {"imported": n, "skipped": [{"key": "...", "reason": "..."}], "mirrors": [MirrorView]}` |

类型：

```text
SettingsView  = { rootDir, intervalMinutes, proxy, githubTokenSet: bool }
SettingsInput = { rootDir, intervalMinutes, proxy, githubToken?: string|null }
                githubToken 缺省或 null → 保持不变；字符串 → 设置（"" 表示清除）
MirrorInput   = { name, repo, folder, enabled, saveChangelog, fileTypes: [], regex, includePrerelease, keepVersions }
MirrorView    = Mirror 全部字段 + "state": MirrorState   (state 至少为 {"status":"idle","files":[]})
VersionView   = { tag, modTime, latest?: bool, files: [{ name, size, url, partial?: bool }] }
                tag 为目录名；按 modTime 降序；不含 *.aria2 控制文件；含 CHANGELOG.md；
                latest = 该目录名等于 sanitize(state.latestTag)（由后端判断，前端不要自行比较 tag）；
                partial = 同目录存在 "<name>.aria2" 控制文件，或该版本为 latest 且 state.files 中同名文件 status != "done"（前端对 partial 文件显示「未完成」且不提供直链）；
                url = "/download/" + 相对 downloadDir 的路径，每段 encodeURIComponent 等价转义（url.PathEscape）
```

`POST /api/mirrors` 与 `PUT` 中 `enabled`、`saveChangelog` 若缺省：新建时默认 `true`；`PUT` 为全量更新（前端总是发送完整对象）。

### 3.7 导入原脚本 YAML（`mirror_import.go`，依赖 `gopkg.in/yaml.v3`）

只读取 `track-list` 映射（忽略 `saved-path`、`proxy`、chown 相关）：

| YAML 字段 | 映射 |
| --- | --- |
| 节点 key | `name` |
| `repo-info` （必填） | `repo` |
| `folder-name` | `folder`（缺省 → 仓库名） |
| `get-changes-info` | `saveChangelog`（缺省 true） |
| `file-type` | `fileTypes` |
| `regex` | `regex` 转换为 `^(?:原值)` —— 原脚本使用 Python `re.match`，从开头锚定，转换后语义保持一致 |

`enabled=true`、`includePrerelease=false`、`keepVersions=0`。逐项校验，失败或与已有镜像的 `folder` 重复 → 进入 `skipped`。被注释掉的条目 YAML 本身就不会解析出来。导入请求本身不触发同步；若检查间隔不为 0，调度器会在下一个 tick（约 30 秒内）依次检查这些从未检查过的镜像（每个镜像一次 API 请求，顺序执行）。想先调整再同步，可先把间隔设为 0 或在导入后停用相应条目。

---

## 4. 前端（新文件 `web/mirror.js`，样式追加到 `web/inject.css`）

- **文件组织**：镜像页面全部逻辑放在独立的 `web/mirror.js`（构建时复制到 `/www/mirror.js`），由 Dockerfile 的 sed 注入在 `inject.js` 之后：`<link rel="stylesheet" href="inject.css"><script src="inject.js"></script><script src="mirror.js"></script></head>`。`inject.js` 保持原有职责不变。
- **侧边栏**：在 `DOMContentLoaded`（早于 Angular bootstrap，inject.js 位于 `<head>` 末尾，AriaNg 脚本都在 `<body>` 中）时，向 `#siderbar-menu`（AriaNg 原拼写）中"Settings"那个 `li.header` 之前插入：
  `<li class="header">GitHub</li><li data-href-match="/mirrors"><a href="#!/mirrors"><i class="fa fa-github"></i> <span>Release 镜像</span></a></li>`
  AriaNg 自身的 `$routeChangeStart` 处理器会根据 `data-href-match` 自动维护高亮。
- **路由**：`angular.module('ariaNg').config(['$routeProvider', ...])` 注册 `/mirrors`，`template` 内联，`controller: 'AriaOneMirrorController'`。须在 bootstrap 前注册（与现有 `app.run` 同一时机）。
- **页面**（使用 AdminLTE / Bootstrap 3 现有类名：`content`、`box`、`table`、`form-control`、`btn`、`label`，自动适配 AriaNg 暗色主题；只在 `inject.css` 中用 `.aria-one-mirror` 作用域补充少量样式）：
  - 顶部工具条：「添加镜像」「全部检查」「导入 YAML」「设置」。
  - 设置面板：根目录、检查间隔（分钟，0=仅手动）、HTTP 代理、GitHub Token（password 输入框，placeholder 显示"已设置，留空保持不变"/"未设置"，另有"清除 Token"复选框）。
  - 添加/编辑表单（行内面板）：名称、仓库（支持粘贴 URL）、文件夹、文件后缀（逗号/空格分隔）、正则、包含预发布、保存 CHANGELOG、保留版本数、启用。提交失败显示后端 `error`。
  - 列表表格：名称+仓库链接、最新版本（链接到 releaseUrl）+发布时间、状态标签（idle 灰 / checking·downloading 蓝 / synced 绿 / incomplete 黄 / error 红，error 时显示 lastError）、文件进度（done/total，下载中显示百分比）、上次检查（相对时间）、操作（立即检查 / 编辑 / 启用开关 / 版本 / 删除）。
  - 「版本」展开行：调用 `/versions`，列出每个版本目录及文件，文件名为 `/download/...` 直链（附大小）。
  - 删除：确认框（使用 `ariaNgCommonService.confirm`，不可用时回退 `window.confirm`），附带"同时删除已下载的文件"选项。
  - 导入面板：textarea 粘贴原 `config.yaml`，显示导入数量与跳过原因。
  - 页面打开期间每 5s 轮询 `GET /api/mirrors`，`$scope.$on('$destroy')` 时取消。
  - 所有请求使用 `$http`，`POST/PUT` 设置 `Content-Type: application/json`；请求体为 `{}` 而非空。
- 可单测的纯函数（如 repo 解析、后缀字符串解析、状态→标签映射、字节格式化、相对时间）放在一个对象中，并在 `typeof module !== 'undefined'` 时通过 `module.exports` 导出；浏览器相关逻辑在 `typeof window === 'undefined'` 时跳过，以便 `node --test web/mirror_test.js` 直接 `require('./mirror.js')`。

---

## 5. 网关与部署

- `nginx.conf.template` 新增：
  ```nginx
  location /api/ {
      proxy_pass http://127.0.0.1:8081/api/;
      proxy_http_version 1.1;
      proxy_set_header Host $host;
      proxy_read_timeout 300s;
  }
  ```
  Basic Auth 在 server 级别，自动覆盖 `/api/`。
- `docker-compose.yml` / README 增加可选 `GITHUB_TOKEN` 环境变量说明；README 增加「GitHub Release 镜像」特性与使用说明（含 YAML 导入、正则语义差异、私有仓库不支持、镜像文件不随任务删除）。

---

## 6. 测试

- Go 单测（`go test ./...`，在 `golang:1.22-alpine` 容器中运行）：规范化/校验、过滤、tag/文件名安全化、Store 原子读写与 Token 不外泄、YAML 导入（含正则锚定转换、重复跳过）、基于 `httptest` 的 mock GitHub + mock aria2 完整同步（addUri 参数、已完成文件跳过、进行中跳过、CHANGELOG、状态流转、限流错误信息）、保留策略、API（CRUD、415、404、400、Token 掩码）、删除拦截保护。
- Node 单测：`node --test web/mirror_test.js` 直接 require `mirror.js` 测纯函数。
- e2e：`tests/e2e_test.sh` 增加 `/api/mirrors` 鉴权、415、CRUD 检查，以及**完全离线的同步链路**：把伪造的 GitHub API 响应与资产文件放进 `/downloads` 下由 Nginx 提供，容器以 `GITHUB_API_URL=http://user:pass@127.0.0.1:8080/download/fake-api` 启动，创建镜像 → 同步 → 轮询到 `synced` → 校验磁盘文件与 CHANGELOG → 通过 RPC `removeDownloadResult` 删除该镜像任务后文件仍存在（删除保护）。`tests/e2e_playwright.js` 增加打开 `#!/mirrors` 页面截图。`tests/docker_build_test.sh` 检查 `/www/mirror.js` 存在。
