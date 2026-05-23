package ws

import "time"

// 协议设计原则：
//   - JSON 文本帧，紧凑字段名（节省带宽，万人时省的就是真金白银）。
//   - 上行（client → server）和下行（server → client）共用一套，由 type 区分。
//   - 关键消息双通道：WSS 是 0 号通道（最高优先级，立即广播）；HTTP 兜底是 1 号通道（仅未连接时落库）。
//
// 消息类型枚举：
//   hello       — 握手后服务端回执（含分配的 ConnID、服务器时间、心跳间隔）
//   ping/pong   — 心跳保活
//   chat        — 聊天消息（最重要）
//   typing      — 输入中
//   read        — 已读
//   sys         — 系统通知（客服上线/离线、分配变更等）
//   error       — 错误反馈

type Envelope struct {
	Type      string      `json:"type"`
	ID        string      `json:"id,omitempty"`         // 消息 ID（UUID）
	From      string      `json:"from,omitempty"`       // visitor:xxx / agent:xxx / sys
	To        string      `json:"to,omitempty"`         // 同上
	ConvID    string      `json:"conv,omitempty"`       // 会话 ID
	Content   string      `json:"content,omitempty"`    // 文本内容
	MediaURL  string      `json:"media,omitempty"`      // 图片/文件 URL（来自 upload API）
	MediaKind string      `json:"mkind,omitempty"`      // image | file
	MediaName string      `json:"mname,omitempty"`      // 原始文件名
	MediaSize int64       `json:"msize,omitempty"`      // 字节
	TS        int64       `json:"ts,omitempty"`         // 毫秒时间戳（北京时间转 UTC ms）
	Priority  int         `json:"prio,omitempty"`       // 0 最高 / 1 次之
	// Node 标记消息来源节点 ID。FanoutToConv 时盖上本节点 ID；
	// fanoutFromRedis 收到自己节点的回环消息时跳过，避免单节点部署的"广播两次"。
	Node      string      `json:"node,omitempty"`
	// ConnID 服务端在转发 chat/read 时盖发起方 connID，让客户端能区分
	// 「自己当前这一端的回声」和「同账号另一端发的」—— 用于多端同步去重。
	ConnID    string      `json:"conn,omitempty"`
	Extra     interface{} `json:"extra,omitempty"`
}

func NowMS() int64 {
	tz, _ := time.LoadLocation("Asia/Shanghai")
	return time.Now().In(tz).UnixMilli()
}
