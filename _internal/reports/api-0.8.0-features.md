# 客服系统 0.8.0 新功能接口契约（内部）

> 适用：后端 Go、iOS App、网页后台（Vue3 + Element Plus 原生样式）、访客端 widget。
> 时间：所有可读时间为北京时间 ISO 8601（+08:00）；WS 的 ts 为 Unix 毫秒。
> 统一响应：成功 {"code":0, "data":..., "total"?}；失败 {"code":<业务码>, "msg":"中文提示"}，HTTP 状态见各接口。
> 鉴权：/api/agent/* 需客服登录（Authorization: Bearer <token> 或 HttpOnly cookie cs_session）；/api/admin/* 另需管理员。
> 方法：CORS 只放行 GET/POST，更新与删除一律用 POST 子路径。

## 0. 公共错误码
| HTTP | code | 含义 |
| --- | --- | --- |
| 400 | 40050 | 参数错误 |
| 400 | 40051 | 选择数量超出上限 |
| 400 | 40052 | 确认文字不正确 |
| 400 | 40053 | 标签不合法（每个 1-16 字，最多 10 个） |
| 400 | 40054 | 备注超过 500 字 |
| 400 | 40055 | 快捷回复内容不合法（标题 ≤32 字，内容 1-1000 字） |
| 400 | 40056 | 搜索关键词需 2-64 个字符 |
| 400 | 40057 | 导出格式不支持（xlsx / txt） |
| 400 | 40058 | 自动清理天数不合法（0 关闭，或 7-3650） |
| 403 | 40310 | 只能撤回自己发送的消息 |
| 403 | 40311 | 无权操作该客户 |
| 403 | 40312 | 团队快捷回复仅管理员可管理 |
| 404 | 40410 | 消息不存在 |
| 404 | 40411 | 快捷回复不存在 |
| 409 | 40910 | 超过 2 分钟，无法撤回 |
| 409 | 40911 | 消息已撤回 |
| 409 | 40912 | 快捷回复数量已达上限 |
| 429 | 42910 | 操作过于频繁，请稍后再试 |
| 500 | 50050 | 服务器错误，请稍后重试 |

## 1. 会话列表（已有接口扩展）GET /api/agent/conversations
- 新增查询参数：tag（按标签精确筛选，可选）。
- 列表与 total 均排除：已删除（回收站中）会话、已拉黑客户。
- 排序：置顶客户在前（按置顶时间倒序），其余按最近活动倒序。
- 每项新增字段：
  - note: string（备注，可能为空串）
  - tags: string[]
  - pinned: bool
  - marked_unread: bool（被手动标为未读）
  - unread: int（已有字段；marked_unread 为 true 时至少为 1）

## 2. 删除聊天记录与回收站（删除单位 = 一位客户的全部聊天记录，含图片文件）
- POST /api/agent/visitors/delete  body {"visitor_ids":["..."]}（1-200 个）
  - 任何客服可用；无权访问的客户跳过。
  - 200 → data {"visitors":n, "conversations":n, "messages":n, "skipped":n}
- POST /api/admin/visitors/delete-all  body {"confirm":"删除全部"}  → 同上 data
- GET  /api/admin/recycle-bin?offset=0&limit=50
  - data: [{"visitor_id","identifier","ip","conversations","messages","deleted_at","deleted_by","purge_at"}]，total
- POST /api/admin/recycle-bin/restore  body {"visitor_ids":[...]}（1-200）→ data {"visitors":n,"conversations":n}
- POST /api/admin/recycle-bin/purge    body {"visitor_ids":[...]}（1-200）或 {"all":true,"confirm":"清空回收站"} → data {"conversations":n,"messages":n,"files":n}
- 规则：回收站保留 7 天，后台每小时自动彻底删除到期数据（含上传文件）；全部操作写审计日志。
- 删除后服务端行为：
  - 在线访客：收到 WS {"type":"history_cleared","ts":...}，访客端清空界面与本地缓存；访客再发消息或重新打开时服务端自动开启新会话（不会以空会话立刻回到列表）。
  - 离线访客：下次打开时 /api/visitor/session 返回的 history_reset_at（毫秒）大于本地记录，访客端清空本地缓存。
  - 所有在线客服：收到 WS {"type":"conversations_deleted","extra":{"visitor_ids":[...]}}（删除全部时 extra 为 {"all":true}），应从列表移除。
  - 恢复：客服端恢复可见；访客端已清空的本地记录不恢复。

## 3. 快捷回复
- GET  /api/agent/quick-replies → data [{"id","scope":"team"|"personal","title","content","sort_order","updated_at"}]（团队 + 我的，团队在前）
- POST /api/agent/quick-replies  body {"scope","title","content","sort_order"?} → data {"id"}；scope=team 需管理员
- POST /api/agent/quick-replies/:id/update  body {"title","content","sort_order"?}
- POST /api/agent/quick-replies/:id/delete
- 限制：标题 ≤32 字（可空）、内容 1-1000 字；个人 ≤100 条/人、团队 ≤200 条；只能改删自己的个人话术，团队话术仅管理员。

## 4. 客户备注与标签
- GET  /api/agent/visitor/:vid/profile → data {"note","tags","pinned","marked_unread","blocked","block_reason","blocked_at","ip_blocked"}
- POST /api/agent/visitor/:vid/profile  body {"note","tags":[...]}（全量覆盖；note ≤500 字；tags ≤10 个、每个 1-16 字、自动去重去首尾空格）
- GET  /api/agent/tags → data [{"tag","count"}]（按使用客户数倒序，最多 100 个）
- 成功修改后服务端向所有在线客服广播 WS {"type":"profile_updated","extra":{"visitor_id"}}，客户端刷新列表。

## 5. 置顶 / 标为未读
- POST /api/agent/visitor/:vid/pin     body {"pinned":true|false}
- POST /api/agent/visitor/:vid/unread  body {"unread":true|false}
- 客服打开会话并标记已读（POST /api/agent/conversations/:id/read）时自动清除 marked_unread。
- 成功后同样广播 profile_updated。

## 6. 拉黑访客
- POST /api/agent/visitor/:vid/block  body {"blocked":true|false, "reason"?: "≤100 字", "block_ip"?: bool}
  - block_ip=true 时同时拉黑该访客当前 IP（以不可逆哈希存储）；之后从该 IP 打开客服窗口的访客（新访客或换身份的访客）自动继承拉黑，出现在黑名单中（原因「同 IP 已被拉黑」），可单独解除。
  - 解除拉黑同时解除由该访客产生的 IP 拉黑，并一并解除因此自动继承拉黑的访客。
- GET  /api/agent/blocked?offset=0&limit=50 → data [{"visitor_id","identifier","ip","reason","blocked_by","blocked_at","ip_blocked"}]，total
- 拉黑后效果（静默）：访客照常可以发消息（不提示被拉黑），但不进会话列表、不响提示音、不推送、不弹来电；记录保留可查。

## 7. 导出聊天记录
- GET  /api/agent/visitor/:vid/export?format=xlsx|txt → 文件下载（Content-Disposition: attachment; filename*=UTF-8''...）
- POST /api/agent/export  body {"visitor_ids":[...]（1-50）, "format":"xlsx"|"txt"} → 文件下载（多个客户合并到一个文件，含「客户」列）
- 内容：时间（北京时间）、客户、发送方（访客/客服/系统）、内容；图片文件写「[图片] 文件名」；已撤回写「[已撤回]」；不含访客浏览页面记录。
- 每次导出最多 50000 条消息；每人每分钟最多 10 次；写审计日志。

## 8. 全局搜索聊天内容
- GET /api/agent/search?q=关键词&limit=30（limit ≤50）
  - data [{"message_id","conv_id","visitor_id","identifier","sender","snippet","created_at"}]，按时间倒序
  - 排除已删除会话、已撤回消息、浏览页面记录；q 长度 2-64；每人每分钟最多 30 次。

## 9. 消息撤回（仅客服撤回自己 2 分钟内的消息）
- POST /api/agent/messages/:id/recall → 200 {"code":0}；错误 40310 / 40410 / 40910 / 40911
- 成功后服务端广播 WS {"type":"recall","id":"<消息id>","conv":"<会话id>","from":"agent:<aid>","ts":...} 给会话内访客与所有在线客服。
- 历史接口（/api/agent/conversations/:id/messages、/api/agent/visitor/:vid/messages）中的消息新增字段 recalled: bool；已撤回消息 content 为空、无附件。
- 客服端显示「你撤回了一条消息」（他人撤回显示「客服撤回了一条消息」）；访客端显示「客服撤回了一条消息」。
- 离线访客：/api/visitor/session 返回 recalled_ids（近 7 天该访客会话内被撤回的消息 id，最多 200 个），访客端据此改写本地缓存。

## 10. 对方正在输入
- 任一端发送 {"type":"typing","ts":...}；服务端以连接绑定的会话为准，覆盖 conv/from；每个连接每秒最多转发 1 次，多余丢弃。
- 访客输入时客服收到 {"type":"typing","from":"visitor:<vid>","conv":"<会话id>"}；客服输入时访客收到 {"type":"typing","from":"agent:<aid>",...}。
- 建议：发送端输入框内容变化时最多每 3 秒发一次；接收端显示「对方正在输入…」，5 秒无新 typing 或收到对方消息后隐藏。

## 11. 自动清理
- 系统设置新增键 auto_clean_days（字符串："0" 关闭；"7"-"3650"），通过已有 /api/admin/settings 读写。
- 每天北京时间 04:30 执行：把最近活动早于 N 天的会话移入回收站（置顶客户除外），7 天后自动彻底删除；执行起止、条数、耗时写日志。

## 12. 访客会话接口（已有接口扩展）POST /api/visitor/session
- 响应新增：history_reset_at（Unix 毫秒，0 表示从未清空）、recalled_ids（string[]）。

## 13. 审计动作名
delete_visitors、delete_all_visitors、recycle_restore、recycle_purge、block_visitor、unblock_visitor、recall_message、export_chats、quick_reply_create、quick_reply_update、quick_reply_delete、update_visitor_profile、auto_clean。
