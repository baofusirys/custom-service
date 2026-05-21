# CHANGELOG

> 每次代码改动完成后，必须立刻在文件顶部追加一条。时间用北京时间绝对格式（YYYY-MM-DD HH:mm）。

---

## [003] 2026-05-21 17:15 — 修复消息内容不显示的严重 Bug + 重做前后台样式

**起因 / 需求**
用户实地测试发现 3 个问题：
1. 严重 Bug：客服后台点开访客，气泡只有空壳，**消息内容看不到**。
2. 前台消息卡片时间显示不明显。
3. 前台和客服窗口的整体样式都不专业、不美观。

**根因分析**
1. Bug 根因：`backend/internal/store/store.go` 中的 `Message`/`Conversation`/`Agent`/`Visitor`/`FileRecord` 5 个结构体都没加 JSON tag。Go 默认按字段名首字母大写序列化（`Content`/`Sender`），而前端按 snake_case 小写读（`m.content`/`m.sender`），永远 undefined。会话列表表面没事，是因为 `ListOpenConversations` 内部用了显式的 `map[string]any`，但 `ListMessages` 直接返回结构体切片，所以消息内容就是看不到。

**改了什么 / 加了什么 / 删了什么**（修改 4 个 / 新增 0 个 / 删除 0 个）
- 修改：[backend/internal/store/store.go](backend/internal/store/store.go) — 给所有外部数据结构加 JSON tag（统一 snake_case）。同时 `pass_hash`/`ip_cipher` 加 `json:"-"` 防止意外外泄。
- 修改：[admin/src/views/Console.vue](admin/src/views/Console.vue) — 整体重做。新增：访客头像（el-avatar，按 visitor_id 哈希出颜色，显示名称首字母）；消息按发送者 + 5 分钟内分组；每组顶部居中时间分隔条（如「今天 16:42」）；hover 气泡显示精确时间；会话列表用 el-avatar + 双行布局；顶部 chat-header 用 el-tag 显示访客地理位置 + 来源 + 当前页；在线统计用 el-statistic。保持所有 Element Plus 组件使用原生默认样式，scoped style 只做布局（无 .el-xxx 覆盖）。
- 修改：[widget/public/chat.html](widget/public/chat.html) — 整体重做。新顶部栏：渐变背景 + 客服头像 + 在线状态指示点（绿色/红色 dot）；消息区按 5 分钟分组 + 时间分隔条 + 圆形头像 + 圆角气泡（mine 渐变蓝 / theirs 白色边框，气泡尾巴用不对称圆角）；系统消息用橙色 chip 风格；文件用 file-card；输入框聚焦时高亮发蓝；滚动条美化；自动 grow textarea。
- 修改：[widget/public/loader.js](widget/public/loader.js) — 浮动按钮从「文字胶囊」改为「56px 圆形渐变图标按钮」+ 未读红色 badge + hover 抬升动效；展开动画 (scale + opacity)。

**业务流程对比**
- 改动前：客服后台打开访客会话，气泡是空的（看不到消息内容）；前台聊天窗口样式像 2010 年的论坛。
- 改动后：消息内容正常显示；前后台都是现代客服系统风格（参考 Intercom/Crisp）；时间清晰可读；消息有头像、有分组、有时间分隔条。

**触发场景与边界 + 验证方式**
- 触发：访问 http://38.76.193.68/admin/console、http://38.76.193.68/widget/demo.html
- 边界：5 分钟内同发送者连续消息合并到同一组；超过 5 分钟或换发送者起新组并显示分隔条。
- 验证：
  1) 访客在 demo.html 发消息 → 客服后台能看到完整内容（而不是空气泡）；
  2) F12 Network 看 `/api/agent/conversations/:id/messages` 响应：字段都是 `content`、`sender` 小写；
  3) 同发送者连续多条消息共享一个头像；
  4) 5 分钟以上间隔出现时间分隔条；
  5) 访客端右下角圆形按钮 + 未读红 badge。

---

## [002] 2026-05-21 16:42 — 首次部署到测试服 38.76.193.68 + 修复 Go 编译 + 修复日志权限

**起因 / 需求**
用户要求把客服系统部署到「零零壹测试服器」（38.76.193.68）做实地测试。部署过程暴露了两个首版骨架未跑通过的问题，必须立即修：
1. Go `go.mod` 只声明了直接依赖，1.17+ 的 module graph 要求完整声明 → `go build` 拒绝构建。
2. Backend 用非 root 用户 `app`（UID 100）运行，bind mount 的宿主目录默认 root 所有，日志写不进去。

**改了什么 / 加了什么 / 删了什么**（修改 1 个 / 新增 0 个 / 删除 0 个）
- 修改：[backend/Dockerfile](backend/Dockerfile#L1-L20) — 把构建顺序从「COPY go.mod → go mod download all → COPY 源码 → go build」改为「COPY 全部源码 → go mod tidy + go mod download → go build」。tidy 会同时补全间接依赖、生成 go.sum、下载所有包。
- 新增（部署运维）：服务器上 chown -R 100:101 /srv/cs-data/logs /srv/cs-data/uploads，匹配容器内 app 用户。
- 新增（不进 git）：本地 `.env`（测试服专用强随机密钥配置）。

**业务流程对比**
- 改动前：`docker compose up -d --build` 在 backend 构建阶段失败：`go: updates to go.mod needed; to update it: go mod tidy`。
- 改动后：6 个镜像全构建成功，6 个容器全 Up（3 个 healthy 健康检查通过），HTTP 200、WSS 握手限速 5r/s 生效、日志 4 路写入正常。

**触发场景与边界 + 验证方式**
- 触发：在测试服执行 `docker compose up -d --build`。
- 边界：构建期间会现场下载所有 Go 依赖 + npm 依赖，第一次约 5 分钟；后续靠 Docker layer cache 加速。
- 验证（在 38.76.193.68 实测，全部通过）：
  1. `curl http://127.0.0.1/api/health` → `{"agents":0,"now":"2026-05-21 16:39:31","status":"ok","tz":"Asia/Shanghai","visitors":0}`
  2. SQL 注入 payload `admin' OR 1=1 --` → 40105 账号或密码错误（未注入成功）
  3. 真实 admin 登录返回 JWT
  4. WSS 握手 20 次连发 → 前 5 次通过、后 15 次 429（限速生效）
  5. security.log 持续记录 ws_handshake_flood violation，count_24h 累加
  6. business.log 持续以北京时间 JSON 格式写入

**已知优化项（不阻塞测试）**
- Nginx 登录限速 `burst=5 nodelay` 偏宽松，10 次并发把 bcrypt 算到 503；下一版可改 `burst=2`。
- curl 客户端会自动 resolve `..`，路径穿越返回 404（被 nginx 兜底），未触达后端的 400 检查；这不是漏洞，仅是测试方法的细节。

---

## [001] 2026-05-21 14:30 — 项目首版：企业级自托管在线客服系统骨架

**起因 / 需求**
用户（爷爷）需要一套可嵌入任意网页的在线客服系统，要求：
1. 访客端可以一行 `<script>` 嵌入任何网站；
2. 客服后台用 Vue3 + Element Plus 原生样式；
3. WSS 全双工通信，万人级长连接 + 消息秒级送达；
4. 客户消息处理顺序以 WSS 通道优先；
5. 自托管 —— 别人想用就部署一份给自己；
6. 部署方式：把仓库目录全量上传到服务器，`docker compose up -d --build` 一条命令搞定，不需要预先 down。

**改了什么 / 加了什么 / 删了什么**（新增功能 9 个 / 删除功能 0 个 / 修改功能 0 个）
- 新增：根目录文档体系 — `LATEST.md`、`CHANGELOG.md`、`README.md`、`.gitignore`、`.env.example`、`docker-compose.yml`
- 新增：`backend/`（Go 后端）— 含 `cmd/server/main.go` 入口、`internal/config`（配置/时区）、`internal/logger`（日志长效存储 + 按天滚动 + 原始 JSON）、`internal/db`（MySQL + 自动迁移）、`internal/redis`（连接池 + Pub/Sub）、`internal/security`（IP 限流、暴力请求拉黑、XSS/SQL 注入清洗、AES-GCM 加密）、`internal/ws`（Hub + Client + 心跳）、`internal/handler`（HTTP API）、`internal/service`（业务逻辑）、`internal/middleware`（JWT/CORS/审计）、`migrations/*.sql`（SQL schema）
- 新增：`admin/`（Vue 3 + Element Plus 客服工作台 + 管理后台）— 含路由 `/admin/login`、`/admin/console`、`/admin/visitors`、`/admin/history`、`/admin/agents`、`/admin/settings`，全部 Element Plus 原生样式，无自定义 CSS
- 新增：`widget/`（嵌入式聊天小部件）— 含 `loader.js` 引导脚本 + iframe 容器 + 内部聊天 UI；通过 `data-cs-endpoint` `data-cs-site` 属性配置；自动重连 + 离线消息暂存
- 新增：`nginx/`（网关 + 反代）— SSL 终结、WSS upgrade、按 IP 限流 + 按 URI 限速、`X-Forwarded-For` 透传、静态资源直出
- 新增：`docs/INTEGRATION.md`、`docs/DEPLOY.md`、`docs/SECURITY.md`、`docs/ARCHITECTURE.md`
- 新增：数据库 schema（visitors / conversations / messages / agents / files / audit_logs / sessions）和启动时自动迁移
- 新增：长效日志体系 — 业务日志、安全日志、审计日志、原始 WSS 报文日志 4 路独立通道，按天 rotate，bind 到宿主机 `/srv/cs-data/logs/`，重启不丢
- 新增：消息优先级队列 — WSS 通道消息走 0 号 channel（最高优先级），HTTP 兜底走 1 号 channel

**业务流程对比**
- 改动前：项目目录是空的，没有任何代码。
- 改动后：用户把仓库目录全量上传服务器，进入目录运行 `docker compose up -d --build`，访问 `https://<域名>/admin/` 即可登录客服工作台；同时给任何第三方网站塞一行 `<script src=".../widget/loader.js" ...>` 即可在该网站右下角出现客服气泡，访客点击即可与客服实时对话。

**触发场景与边界 + 验证方式**
- 触发：访客打开任何嵌入 widget 的网站 → 触发 visitor session 创建 → WSS 握手 → 双向消息流。
- 边界：
  - 单 IP 限流：60 req/min（HTTP）+ 5 次握手/分钟（WSS）；超过返回 429。
  - 单访客消息频率：10 条/分钟，超过临时静音 60 秒并打安全日志。
  - 文件上传上限：默认 20 MB，类型白名单。
  - 客服离线时，访客消息进入「未分配队列」并持久化到 MySQL，客服上线后自动接管。
- 验证方式：
  1. `docker compose up -d --build` 后所有容器 healthy；
  2. `curl https://<域名>/api/health` 返回 `{"status":"ok","tz":"Asia/Shanghai"}`；
  3. 浏览器开 demo 页（`/widget/demo.html`）发送消息，客服后台秒级收到；
  4. 安全自验证：`bash docs/security-selftest.sh`（含 SQL 注入 payload、XSS payload、单 IP 暴力请求模拟，应全部被拦截）；
  5. 重启 `docker compose restart`，历史消息和日志均不丢。
