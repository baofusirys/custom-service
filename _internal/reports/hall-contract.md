# 多人大厅（类谷歌会议）接口契约 v1（[119]，北京时间 2026-10-17）

> 本文件是后端、访客网页窗口、网页后台、Flutter App 四端共同遵守的唯一契约。改动任何字段必须同步更新本文件。
> 标注「补充（后端实现）」的条目是后端实现时补齐的细节，不改变原语义；标注「修订（主代理确认）」的条目是主代理统一决定后覆盖原文的条目，并写明原因；标注「待确认：」的条目需要主代理/爷爷拍板。

## 1. 产品规则（爷爷已确认）
- 访客打开网站即自动加入「大厅」：默认**关麦**，但**能听到**大厅里所有已开麦的人（受浏览器自动播放限制，见 7.3）。
- 大厅与现有一对一私聊**并存**：窗口内两个页签「大厅」「私聊客服」，默认显示大厅；隐私问题走私聊。
- 大厅里可发文字、图片、文件；可语音；**访客可随时自己开麦**（受同时开麦人数上限约束）。
- 视频：管理员在系统设置里随时开/关。开启后**客服和访客都可以开摄像头**（受同时开摄像头人数上限约束）；关闭时服务端立刻撤销所有人的摄像头权限。
- 客服可：单人静音、全员静音（只对访客）、关闭某人摄像头、移出大厅（禁止 N 分钟再进）、拉黑（复用现有拉黑）、删除大厅消息。
- 大厅消息保存到数据库；新进入的人看到最近 50 条；客服删除的消息对所有人消失。
- 客服端：网页后台与手机 App 都能进入大厅（听、说、看、发消息、管理）。
- 带宽：服务器公网 7Mbps 且与主站共用 → 默认上限：同时开麦访客 6 人（客服不占名额）、同时开摄像头 2 人（含客服）、单个参与者最多同时订阅 2 路视频；视频发布 360p 并带 180p 联播层，观看端默认只收低清层。

## 2. 身份与隐私
- 房间名：`hall_<site_id>`（当前只有 `hall_default`）。
- LiveKit identity（**不可泄露访客 vid**：vid 可用于恢复访客会话，等同凭证）：
  - 访客：`v_` + hex(HMAC-SHA256(由 DATA_AES_KEY 派生的独立子密钥, vid))[:20]
  - 客服：`a_<agent_id>`
- 显示名 name：访客 `访客 NNNN`（NNNN 取同一 HMAC 的 4 位十进制，稳定），客服 `客服·<昵称或用户名>`。**大厅里不暴露访客 IP、地区、identifier、vid。**
- participant metadata（JSON 字符串，服务端签发、客户端不可改）：`{"role":"visitor"|"agent","code":"NNNN"}`；客服需要的 vid 映射只通过客服专用接口 `/api/agent/hall/state` 返回。
- 补充（后端实现）：
  - 子密钥 = HKDF-SHA256(IKM=DATA_AES_KEY 原始 32 字节, salt=空, info=`custom-service/hall/identity/v1`, 32 字节)，只在后端内存中使用；NNNN = HMAC 结果第 17–20 字节按大端 uint32 取模 10000，左补零到 4 位。不同访客的 NNNN 可能重复（只用于显示），唯一标识以 identity 为准。
  - identity 格式校验：访客 `^v_[0-9a-f]{20}$`，客服 `^a_[0-9]{1,19}$`；其它一律拒绝。
  - 客服 metadata 为 `{"role":"agent","code":""}`。
  - 同一访客多标签页使用同一 identity：后加入的连接会把旧连接挤下线（LiveKit 同 identity 规则）。

## 3. 设置项（复用现有 `GET/POST /api/admin/settings`，加入白名单与校验）
| 键 | 默认 | 取值 | 说明 |
| --- | --- | --- | --- |
| `hall_enabled` | `true` | `true`/`false` | 关闭后不再签发大厅令牌，已在线者被移出 |
| `hall_video_enabled` | `false` | `true`/`false` | 关闭时撤销所有人摄像头权限 |
| `hall_max_speakers` | `6` | 1–20 | 同时开麦**访客**上限（客服不占） |
| `hall_max_cameras` | `2` | 0–6 | 同时开摄像头上限（含客服） |
| `hall_max_participants` | `100` | 10–500 | 大厅人数上限，满员时 join 返回 40977（访客仍可用私聊） |

访客公开设置 `GET /api/visitor/settings` 追加 `hall_enabled`、`hall_video_enabled`。设置变更后服务端通过 WS 向所有访客与客服广播 `hall_settings`（见 5）。
- 补充（后端实现）：
  - 取值去掉首尾空白后必须是上表的精确字符串（布尔只接受 `true`/`false`，整数只接受十进制且在范围内），按规范化值保存（例如 ` 6` 保存为 `6`、`06` 保存为 `6`）；任一大厅键不合法时整次保存失败，返回 400 `40059`（msg 指出哪个键），不会部分保存。
  - `GET /api/visitor/settings` 的 `data.hall_enabled`、`data.hall_video_enabled` 是 JSON 布尔值；未配置 `LIVEKIT_API_KEY` 时 `hall_enabled` 恒为 `false`。
  - `GET /api/admin/settings` 原样返回已保存的字符串；迁移 015 会写入上表默认值（已有值不覆盖），因此 5 个键始终存在。
  - 调低 `hall_max_speakers`/`hall_max_cameras` 不会强制收回已占用的名额，只阻止新的占用，直到人数降到上限以下。
  - 关闭 `hall_enabled`：立即广播 `hall_settings`，并在后台把所有参与者移出（并发有上限），之后由 30 秒对账兜底；关闭 `hall_video_enabled`：后台立即收回全部摄像头权限并清空摄像头名额，对账兜底。

## 4. HTTP 接口（统一响应 `{code, msg, data}`；鉴权沿用各自现有方式）
### 4.1 访客（修订（主代理确认）：请求头 `Authorization: Bearer <visitor_token>` 鉴权）
- 修订（主代理确认）：原文「与 `/api/visitor/turn-credential` 相同的访客鉴权」有缺陷——该接口实际不鉴权、只读 URL 参数 `?vid=`，而 vid 等同凭证不能进 URL。大厅访客接口统一按请求头 `Authorization: Bearer <visitor_token>`（`/api/visitor/session` 返回的 `visitor_token`，`/api/upload` 已在用）鉴权，vid/site_id 一律从 token 取，URL 与请求体都不带 vid。缺少或无效令牌返回 401 `40110`。
- `POST /api/visitor/hall/join` → `data:{url, token, room, identity, name, video_enabled, max_speakers, max_cameras, ice_servers:[{urls,username,credential}], history:[HallMessage…最多 50，正序]}`
  - 403 `40371` 大厅已关闭；403 `40372` 已被移出（`data.retry_after` 秒）；409 `40977` 大厅已满；429 `42910` 过于频繁。
  - 令牌 TTL 6 小时；权限：可订阅、不可发布、不可发 data、不可改自身 metadata。
  - 补充（后端实现）：`url` 为 `LIVEKIT_PUBLIC_URL`（默认 `wss://<PUBLIC_DOMAIN>/lk`）；`ice_servers` 为现有 coturn 短期凭证（TURN 未配置时为空数组 `[]`，客户端用 LiveKit 默认值），TURN 用户名里的标识用 identity 而不是 vid；`video_enabled` 为布尔，`max_speakers`/`max_cameras` 为整数；被拉黑访客照常可加入收听（静默）。限流：每访客 10 次/分钟。
- `POST /api/visitor/hall/mic` `{on:bool}` → `data:{on, used, max}`；409 `40973` 开麦人数已满；403 `40374` 当前无法开麦（已拉黑/被移出/未在大厅）。
  - on=true：服务端占名额并通过 LiveKit `UpdateParticipant` 授予 `MICROPHONE` 发布权限，客户端随后发布麦克风；on=false：客户端先停止发布，服务端释放名额并收回权限。
  - 补充（后端实现）：`used`=当前已占用的开麦名额数（访客），`max`=`hall_max_speakers`；「未在大厅」以 LiveKit 中查不到该参与者为准（客户端必须先连上 LiveKit 再调用）；大厅关闭返回 403 `40371`；LiveKit 不可用返回 503 `50370`（已占的名额会回滚）；同一身份的开麦/摄像头操作串行化，并发重复请求返回 429 `42910`；请求体不合法返回 400 `40060`；限流：每访客 20 次/分钟。
  - 补充（后端实现）：名额是 120 秒租约，只有「正在发布且未静音」时由 30 秒对账续租；麦克风/摄像头处于静音或未发布状态超过约 2 分钟会自动释放名额并收回权限（之后需要重新调用 on=true）。没有名额却在发布的参与者会被对账收回权限。
- `POST /api/visitor/hall/camera` `{on:bool}` → 同上；403 `40375` 视频未开启；409 `40976` 摄像头人数已满。
  - 补充（后端实现）：`max`=`hall_max_cameras`，为 0 时直接 409 `40976`；`used` 含客服占用。
- `GET /api/visitor/hall/messages?before=<id>&limit=50` → `data:[HallMessage…正序]`（limit ≤ 50）。
  - 补充（后端实现）：`before` 为可选的 HallMessage id（UUID），返回严格早于它的消息；`limit` 1–50，缺省或越界按 50；`before` 格式不合法 400 `40060`；大厅关闭 403 `40371`、被移出 403 `40372`；限流：每访客 30 次/分钟。访客看得到自己被静默的消息（见 4.2 拉黑），看不到别人的。
### 4.2 客服（现有客服鉴权）
- `POST /api/agent/hall/join` → 同访客结构；客服令牌初始即授予 `MICROPHONE` 发布权限（不占名额）。
  - 补充（后端实现）：客服不受 `hall_max_participants` 限制；被管理员移出的客服在禁入期内返回 403 `40372`；客服令牌同样不可发 data、不可改 metadata、不含 roomAdmin。限流：每客服 10 次/分钟。
- `POST /api/agent/hall/camera` `{on}` → 同访客摄像头规则（占名额）。
- `GET /api/agent/hall/state` → `data:{settings:{5 项}, counts:{participants, speakers, cameras}, participants:[{identity, name, role, vid?, agent_id?, mic_on, camera_on, joined_at, blocked}]}`（vid 只在客服接口返回）。
  - 补充（后端实现）：`settings` 键为 `enabled`、`video_enabled`（布尔）、`max_speakers`、`max_cameras`、`max_participants`（整数）；`counts.speakers` 是访客开麦名额占用数，`counts.cameras` 是摄像头名额占用数；`mic_on`/`camera_on` 表示该参与者当前正在发布且未静音的麦克风/摄像头；`joined_at` 为带 +08:00 的 ISO 8601；`agent_id` 为字符串；`blocked` 只对访客有意义。另有 `data.degraded`（布尔）：LiveKit 暂不可用时为 `true`，此时参与者列表来自后端缓存，`mic_on`/`camera_on` 按名额推断。限流：每客服 60 次/分钟。
- `POST /api/agent/hall/moderate` `{action, identity?, minutes?}`：`mute`（收回麦克风权限并释放名额）、`mute_all`（全部访客）、`camera_off`、`kick`（移出并禁入 minutes 分钟，默认 30，范围 1–1440）。全部写审计日志。
  - 补充（后端实现）：返回 `data:{action, identity, affected}`（`affected` 为实际被处理的人数）；对象不在大厅时照常释放名额并返回 `affected:0`（幂等）；`kick` 即使 LiveKit 暂不可用也会先写入禁入，并返回 503 `50370`（msg 说明移出会在恢复后由对账自动生效）。对象规则：不能对自己操作；普通客服只能操作访客；管理员可以操作访客和其他客服（客服被 `mute` 后需重新加入大厅才恢复麦克风权限）；违反返回 403 `40376`。参数不合法 400 `40060`；限流：每客服 60 次/分钟。
- `GET /api/agent/hall/messages?before=<id>&limit=50`；`POST /api/agent/hall/messages/:id/delete`（软删除并广播）。
  - 补充（后端实现）：任一客服均可删除大厅消息；消息不存在 404 `40477`；已删除再删返回成功（幂等，不重复广播）。客服历史不含被静默的消息。
- 拉黑：复用 `POST /api/agent/visitor/:vid/block`；被拉黑访客在大厅「静默」：仍可收听，开麦/摄像头返回 40374，发出的大厅消息只回显给自己（不广播、不提醒）。
  - 补充（后端实现）：拉黑生效时如果该访客正占用开麦/摄像头名额，会立即收回权限并释放名额；静默消息照常入库（`deleted_by='silenced'`、`deleted_at`=发送时间），只出现在该访客自己的历史里，30 天后随大厅消息一起清理。
### 4.3 LiveKit 回调
- `POST /api/livekit/webhook`（仅 cs-livekit 调用）：校验 `Authorization` JWT（HS256，API Secret 签名，`iss`=API Key，claim `sha256`=请求体 SHA256 的 base64）。处理 `participant_joined/left`（记日志、释放名额）、`track_published`（视频关闭或超限时立即收回权限）。
  - 补充（后端实现）：`Authorization` 头是不带 `Bearer ` 前缀的原始 JWT（与 livekit/protocol webhook 包一致，后端也兼容带前缀）；必须带 `exp`，允许 60 秒时钟偏差；请求体上限 1 MB；同一事件 `id` 10 分钟内只处理一次（防重放）。成功 200 `{code:0}`；签名/校验失败 401 `40120`；经反向代理转发（带 `X-Forwarded-For`/`X-Real-IP`）的请求一律 404。cs-nginx 对 `/api/livekit/` 一律 404，cs-livekit 在容器内网直连 `http://cs-backend:8080/api/livekit/webhook`。另处理 `participant_connection_aborted`（同 left）、`room_finished`（清空在线与名额）。
### 4.4 大厅附件上传（补充（后端实现），主代理确认）
- 复用 `POST /api/upload`（multipart）：`file` + `uploader=visitor|agent` + `scope=hall`，不带 `conv_id`；返回结构与现有完全一致 `{code:0, id, url:"/files/...?access=...", kind, size, name, mime}`；类型/大小白名单与现有完全一致。
- 鉴权：访客 `Authorization: Bearer <visitor_token>`；客服 `Authorization: Bearer <agent_token>` 或 HttpOnly `cs_session` Cookie（与客服中间件一致，并校验账号启用与令牌版本）。
- `scope` 只接受空或 `hall`，其它值 400 `40061`。客服令牌过期返回 401 `40102`（前端可续期），无效/被吊销返回 401 `40108`（与现有一致）。
- `scope=hall` 时：大厅必须开启（否则 403 `40371`）；访客不能处于禁入期（403 `40372`）；被拉黑访客允许上传（静默，消息只回显自己）；客服跳过 conv_id 归属校验。
- `hall_chat` 的媒体 URL 只接受本接口以 `scope=hall` 签发、且上传者就是发送者本人的文件 URL（防止把私聊文件链接贴进大厅）；大厅附件 30 天后随大厅消息清理（数据库记录与磁盘文件）。

## 5. WebSocket 信封（复用现有 `/ws/visitor`、`/ws/agent` 连接）
- 订阅：客户端在 LiveKit 连接成功后发送 `{type:"hall_subscribe"}`，服务端校验（大厅开启、未被移出）后把该连接加入大厅广播集合；`{type:"hall_unsubscribe"}` 退出。
  - 修订（主代理确认）：订阅帧带 `id`（UUID）：`{type:"hall_subscribe", id}`；成功回 `{type:"hall_subscribed", id}`，失败回 `{type:"error", id, content:<中文原因>}`。`{type:"hall_unsubscribe", id}` 成功回 `{type:"hall_unsubscribed", id}`。连接断开自动退出广播集合；被移出时服务端也会把该访客的连接移出广播集合。
- 发消息：`{type:"hall_chat", id:<uuid v4>, content, media?, mkind?("image"|"file"), mname?, msize?}`
  - 修订（主代理确认）：WS 信封沿用现有 `ws.Envelope` 的 JSON 字段名，大厅消息的媒体字段在 WS 里是 `media`（URL）、`mkind`、`mname`、`msize`，而不是原文的 `media_url/media_kind/media_name/media_size`；HTTP 返回的 HallMessage（第 6 节）保持 `media_url/media_kind/media_name/media_size` 不变。
  - 成功 `{type:"hall_ack", id}`；失败 `{type:"error", id, content:<中文原因>}`。
  - 服务端校验：大厅开启、已订阅、未被移出；内容 1–1000 字并清洗；媒体只接受本系统上传返回的 URL；限流：每访客 5 条/10 秒（超限记安全日志）。
  - 修订（主代理确认）：原文「+ 每 IP 20 条/分钟」取消，改为只按身份限流：每访客 5 条/10 秒、每客服 20 条/10 秒。原因：爷爷早先已决定「所有按 IP 的限流全部去掉」（生产编排注释 [066]），多人共用出口 IP（公司/校园/运营商 NAT）时按 IP 限流会误伤。
  - 补充（后端实现）：`hall_ack` 额外带服务器时间 `ts`（毫秒）；`id` 必须是 UUID v4，重复 `id`（同一发送者同一内容）视为重试，只回 ack 不重复广播，不同发送者/内容复用同一 `id` 返回 error「消息标识冲突」；无媒体时内容 1–1000 字，带媒体时内容可为空（0–1000 字）；`mkind/mname/msize` 一律以服务端文件记录为准，客户端传的值被忽略；静默（被拉黑）访客发送的消息也回 `hall_ack`，并只把 `hall_chat` 回显给该连接。
- 广播：`{type:"hall_chat", id, content, media?, mkind?, mname?, msize?, ts, extra:{sender:"visitor"|"agent", identity, name, role}}`
  - 修订（主代理确认）：媒体字段名同上（`media/mkind/mname/msize`）；发送者信息放 `extra:{sender, identity, name, role}`，时间放 `ts`（Unix 毫秒）。
- 删除：`{type:"hall_deleted", id}`；设置：`{type:"hall_settings", extra:{enabled, video_enabled, max_speakers, max_cameras}}`
- 定向通知（被操作者）：`{type:"hall_moderation", extra:{action:"muted"|"camera_off"|"kicked"|"mute_all", minutes?}}`
  - 补充（后端实现）：`mute_all` 发给所有已订阅大厅的访客连接；其余动作发给被操作者的全部连接。`hall_settings` 发给所有在线访客与客服连接（不要求已订阅）。
- 多副本经 Redis 频道广播（与现有 FanoutToConv 同机制）。

## 6. HallMessage
`{id, sender:"visitor"|"agent"|"system", identity, name, role, content, media_url, media_kind, media_name, media_size, created_at(带 +08:00 的 ISO 8601)}`
- 表 `hall_messages`（迁移 015，含 down）：`id CHAR(36) PK, room VARCHAR(40), sender ENUM, sender_ref VARCHAR(64)（访客 vid / 客服 id，仅服务端用）, identity VARCHAR(40), name VARCHAR(64), content TEXT, media_url/kind/name/size, created_at DATETIME(3), deleted_at DATETIME(3) NULL, deleted_by VARCHAR(64)`，索引 `(room, created_at)`、`(sender, sender_ref, created_at)`。
- 自动清理：复用现有每日任务，大厅消息保留 30 天。
- 补充（后端实现）：`created_at` 格式 `2006-01-02T15:04:05.000+08:00`；`media_url/media_kind/media_name` 无值时为空串，`media_size` 无值时为 0；`role` 与 `sender` 取值相同；`sender_ref` 永不出现在任何接口返回里。列定义：`sender ENUM('visitor','agent','system')`、`media_url VARCHAR(512) NULL`、`media_kind VARCHAR(16) NULL`、`media_name VARCHAR(256) NULL`、`media_size BIGINT NULL`、`deleted_by VARCHAR(64) NOT NULL DEFAULT ''`；另加索引 `(created_at)` 供每日清理。每日北京时间 04:30 的维护任务分批删除 30 天前的大厅消息与大厅附件。

## 7. 媒体服务（LiveKit 自托管）
### 7.1 服务
- 镜像 `cs-livekit`：`FROM livekit/livekit-server:v1.13.10`（固定版本），配置文件 COPY 进镜像；密钥只走环境变量 `LIVEKIT_KEYS`。
- 端口：信令 7880 仅容器内网（cs-nginx 反代 `/lk/` → `http://cs-livekit:7880/`，WebSocket）；媒体 `7881/tcp`、`7882/udp`（单端口复用）对公网（需爷爷在腾讯云安全组放行）。
- `rtc.node_ip` = 服务器公网 IP（环境变量）；不启用内置 TURN；客户端 ICE 服务器由后端随令牌下发现有 coturn 短期凭证。
- 资源：`mem_limit 512m`、`cpus 1.0`、日志轮转、`no-new-privileges`、健康检查。
- 补充（后端实现）：cs-nginx 只反代 `/lk/rtc` 开头的路径（信令 WebSocket 与 `/rtc/validate`），`/lk/` 下其它路径（含 `/twirp` 服务端 API）一律 404；容器内 `LIVEKIT_KEYS` 由入口脚本从 `LIVEKIT_API_KEY`/`LIVEKIT_API_SECRET` 组装（仍只走环境变量），webhook 的 api_key 走 `LIVEKIT_WEBHOOK_API_KEY`；公网 IP 走 `NODE_IP`（缺失或格式不对时容器拒绝启动）。生产机（2 核 7.5G 与主站共用）片段取 `mem_limit 384m`、`cpus 0.75`，详见 `_internal/reports/hall-deploy.md`。
### 7.2 后端配置（`.env`，不入库）
- `LIVEKIT_API_KEY`、`LIVEKIT_API_SECRET`（≥32 字符随机，命中占位黑名单则 FATAL）、`LIVEKIT_INTERNAL_URL`（默认 `http://cs-livekit:7880`）、`LIVEKIT_PUBLIC_URL`（默认 `wss://<PUBLIC_DOMAIN>/lk`）。
- 未配置 `LIVEKIT_API_KEY` 时大厅整体关闭（join 返回 40371，启动日志 WARN），不影响原有功能。
- 补充（后端实现）：`LIVEKIT_API_KEY` 只允许 3–64 位字母、数字、`_`、`-`；`LIVEKIT_INTERNAL_URL` 必须是 http(s) 地址；`LIVEKIT_PUBLIC_URL` 必须是 ws(s) 地址；不合法同样 FATAL。后端调用 LiveKit 服务端 API（Twirp JSON）单次超时 5 秒、全进程并发上限 8、连接复用。
### 7.3 浏览器限制（界面必须处理）
- 浏览器禁止网页在用户没点过页面时自动出声：访客端先自动连接大厅并订阅音频；若 `room.canPlaybackAudio` 为 false，显示「点击收听大厅」并在第一次点击时 `room.startAudio()`。iOS Safari 同样需要一次点击；麦克风/摄像头需要用户授权。

## 8. 错误码
40371 大厅已关闭 · 40372 已被移出 · 40374 当前无法开麦/开摄像头 · 40375 视频未开启 · 40973 开麦已满 · 40976 摄像头已满 · 40977 大厅已满 · 42910 过于频繁
- 补充（后端实现）：40059 大厅设置不合法 · 40060 参数错误 · 40061 上传 scope 不合法 · 41302 回调请求体过大 · 40110 访客令牌缺失或无效 · 40120 回调签名无效 · 40376 不能对该对象执行此操作 · 40477 大厅消息不存在 · 50370 语音服务暂不可用 · 50050 服务器错误（沿用现有）。
