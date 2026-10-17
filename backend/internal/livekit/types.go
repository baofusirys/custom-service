package livekit

import (
	"encoding/json"
	"strconv"
	"strings"
)

// LiveKit 的 JSON 由 protojson 生成：默认字段名是 lowerCamelCase（joinedAt），但 Twirp 服务端也可能按
// proto 原名（joined_at）输出；int64 在 protojson 里是字符串；枚举一般是名字字符串，也兼容数字。
// 这里统一用宽松解码，两种字段名都接受，避免上游配置差异导致静默解析失败。

// TrackInfo 对照 livekit_models.proto TrackInfo（只取用到的字段）。
type TrackInfo struct {
	Sid    string
	Type   string // AUDIO | VIDEO | DATA
	Source string // CAMERA | MICROPHONE | SCREEN_SHARE | SCREEN_SHARE_AUDIO | UNKNOWN
	Muted  bool
}

// Permission 对照 livekit_models.proto ParticipantPermission（只取用到的字段）。
type Permission struct {
	CanSubscribe      bool
	CanPublish        bool
	CanPublishData    bool
	CanPublishSources []string // 枚举名（大写）
	CanUpdateMetadata bool
	Hidden            bool
}

// ParticipantInfo 对照 livekit_models.proto ParticipantInfo（只取用到的字段）。
type ParticipantInfo struct {
	Sid        string
	Identity   string
	Name       string
	State      string // JOINING | JOINED | ACTIVE | DISCONNECTED
	Metadata   string
	JoinedAt   int64 // 秒
	Tracks     []TrackInfo
	Permission *Permission
}

// Publishing 该参与者是否正在发布指定来源且未静音。
func (p *ParticipantInfo) Publishing(source string) bool {
	if p == nil {
		return false
	}
	for _, t := range p.Tracks {
		if t.Source == source && !t.Muted {
			return true
		}
	}
	return false
}

// HasTrack 该参与者是否有指定来源的已发布轨道（含静音）。
func (p *ParticipantInfo) HasTrack(source string) bool {
	if p == nil {
		return false
	}
	for _, t := range p.Tracks {
		if t.Source == source {
			return true
		}
	}
	return false
}

// CanPublishSource 按 LiveKit 语义判断参与者当前是否被允许发布某来源：
// canPublish=false 一律不允许；canPublishSources 为空表示允许全部来源。
func (p *ParticipantInfo) CanPublishSource(source string) bool {
	if p == nil || p.Permission == nil || !p.Permission.CanPublish {
		return false
	}
	if len(p.Permission.CanPublishSources) == 0 {
		return true
	}
	for _, s := range p.Permission.CanPublishSources {
		if s == source {
			return true
		}
	}
	return false
}

var (
	trackSourceNames = map[int64]string{0: "UNKNOWN", 1: SourceCamera, 2: SourceMicrophone, 3: "SCREEN_SHARE", 4: "SCREEN_SHARE_AUDIO"}
	trackTypeNames   = map[int64]string{0: "AUDIO", 1: "VIDEO", 2: "DATA"}
	stateNames       = map[int64]string{0: "JOINING", 1: "JOINED", 2: "ACTIVE", 3: "DISCONNECTED"}
)

// rawObject 宽松读取 JSON 对象字段（多个候选键名取第一个存在的）。
type rawObject map[string]json.RawMessage

func (o rawObject) pick(keys ...string) (json.RawMessage, bool) {
	for _, k := range keys {
		if v, ok := o[k]; ok && len(v) > 0 && string(v) != "null" {
			return v, true
		}
	}
	return nil, false
}

func (o rawObject) str(keys ...string) string {
	v, ok := o.pick(keys...)
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	return ""
}

func (o rawObject) int64(keys ...string) int64 {
	v, ok := o.pick(keys...)
	if !ok {
		return 0
	}
	var n json.Number
	dec := json.NewDecoder(strings.NewReader(string(v)))
	dec.UseNumber()
	if dec.Decode(&n) == nil {
		if i, err := n.Int64(); err == nil {
			return i
		}
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i
		}
	}
	return 0
}

func (o rawObject) boolean(keys ...string) bool {
	v, ok := o.pick(keys...)
	if !ok {
		return false
	}
	var b bool
	return json.Unmarshal(v, &b) == nil && b
}

// enum 读取枚举：名字字符串原样返回（转大写），数字按映射表转名字。
func (o rawObject) enum(names map[int64]string, keys ...string) string {
	v, ok := o.pick(keys...)
	if !ok {
		return names[0]
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		return strings.ToUpper(s)
	}
	var n int64
	if json.Unmarshal(v, &n) == nil {
		if name, ok := names[n]; ok {
			return name
		}
	}
	return ""
}

func (o rawObject) enumList(names map[int64]string, keys ...string) []string {
	v, ok := o.pick(keys...)
	if !ok {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(v, &items) != nil {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		holder := rawObject{"v": item}
		if name := holder.enum(names, "v"); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func (o rawObject) object(keys ...string) (rawObject, bool) {
	v, ok := o.pick(keys...)
	if !ok {
		return nil, false
	}
	var obj rawObject
	if json.Unmarshal(v, &obj) != nil {
		return nil, false
	}
	return obj, true
}

func (o rawObject) objects(keys ...string) []rawObject {
	v, ok := o.pick(keys...)
	if !ok {
		return nil
	}
	var arr []rawObject
	if json.Unmarshal(v, &arr) != nil {
		return nil
	}
	return arr
}

func decodeTrack(o rawObject) TrackInfo {
	return TrackInfo{
		Sid:    o.str("sid"),
		Type:   o.enum(trackTypeNames, "type"),
		Source: o.enum(trackSourceNames, "source"),
		Muted:  o.boolean("muted"),
	}
}

func decodePermission(o rawObject) *Permission {
	return &Permission{
		CanSubscribe:      o.boolean("canSubscribe", "can_subscribe"),
		CanPublish:        o.boolean("canPublish", "can_publish"),
		CanPublishData:    o.boolean("canPublishData", "can_publish_data"),
		CanPublishSources: o.enumList(trackSourceNames, "canPublishSources", "can_publish_sources"),
		CanUpdateMetadata: o.boolean("canUpdateMetadata", "can_update_metadata"),
		Hidden:            o.boolean("hidden"),
	}
}

func decodeParticipant(o rawObject) ParticipantInfo {
	p := ParticipantInfo{
		Sid:      o.str("sid"),
		Identity: o.str("identity"),
		Name:     o.str("name"),
		State:    o.enum(stateNames, "state"),
		Metadata: o.str("metadata"),
		JoinedAt: o.int64("joinedAt", "joined_at"),
	}
	for _, t := range o.objects("tracks") {
		p.Tracks = append(p.Tracks, decodeTrack(t))
	}
	if perm, ok := o.object("permission"); ok {
		p.Permission = decodePermission(perm)
	}
	return p
}

// UnmarshalJSON 宽松解码（camelCase / snake_case 均可）。
func (p *ParticipantInfo) UnmarshalJSON(b []byte) error {
	var o rawObject
	if err := json.Unmarshal(b, &o); err != nil {
		return err
	}
	*p = decodeParticipant(o)
	return nil
}

// UnmarshalJSON 宽松解码。
func (t *TrackInfo) UnmarshalJSON(b []byte) error {
	var o rawObject
	if err := json.Unmarshal(b, &o); err != nil {
		return err
	}
	*t = decodeTrack(o)
	return nil
}

// permissionRequest UpdateParticipant 请求里的 permission（Twirp JSON 接受 proto 原名）。
// 每个字段都显式写出：LiveKit 用整份 permission 覆盖参与者权限。
type permissionRequest struct {
	CanSubscribe      bool     `json:"can_subscribe"`
	CanPublish        bool     `json:"can_publish"`
	CanPublishData    bool     `json:"can_publish_data"`
	CanPublishSources []string `json:"can_publish_sources"`
	Hidden            bool     `json:"hidden"`
	CanUpdateMetadata bool     `json:"can_update_metadata"`
}

// PermissionFor 根据允许发布的来源（Source* 大写枚举名）生成完整权限：
// 可订阅、不可发 data、不可改 metadata；来源为空时 can_publish=false（绝不发送「can_publish=true + 空来源」，那等于允许全部来源）。
func PermissionFor(sources []string) Permission {
	p := Permission{CanSubscribe: true}
	if len(sources) > 0 {
		p.CanPublish = true
		p.CanPublishSources = append([]string(nil), sources...)
	}
	return p
}

func (p Permission) request() permissionRequest {
	sources := p.CanPublishSources
	if sources == nil {
		sources = []string{}
	}
	return permissionRequest{
		CanSubscribe:      p.CanSubscribe,
		CanPublish:        p.CanPublish && len(sources) > 0,
		CanPublishData:    false,
		CanPublishSources: sources,
		Hidden:            false,
		CanUpdateMetadata: false,
	}
}
