// Package push 集成 messagepush.luckfast.com 的 APNs 中转推送服务。
//
// 工作原理：用户在 App Store 装「消息推送助手」App，拿到自己的 User ID + User Key。
// 我们后端用 HTTP POST 调他们的 API，他们的 App 用自己的 APNs 证书把推送送到用户 iPhone。
// 对自托管客服系统的好处：不需要自己买 Apple Developer 账号、不需要 .p8 Key，免费。
// 局限：推送显示的是「消息推送助手」App 的图标，不是我们自己 App。
//
// 配置：通过 admin Settings 的 push_user_id / push_user_key 两项设置，留空则跳过推送。
package push

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"
)

// luckfastBaseURL 是 messagepush 的发送 endpoint，路径模式 /send/<UserID>/<UserKey>。
const luckfastBaseURL = "https://messagepush.luckfast.com/send"

// Client 是 luckfast 推送的 HTTP 客户端。无状态可复用，全局单例够用。
type Client struct {
	httpClient *http.Client
	log        *zap.Logger
}

// NewClient 创建 luckfast 推送客户端。log 用于记录成功/失败。
func NewClient(log *zap.Logger) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 8 * time.Second},
		log:        log,
	}
}

// Options 是 Send 的参数集。
type Options struct {
	UserID   string // luckfast User ID（必填，留空跳过推送）
	UserKey  string // luckfast User Key（必填，同上）
	Title    string // 推送标题
	Subtitle string // 副标题（可选）
	Message  string // 内容（必填，空则跳过）
	JumpURL  string // 点击推送跳转的 URL（可选）
	Sound    string // 提示音编号 "0"-"9"（可选，默认 "9"——爷爷截图选的）
}

// Send 发起一次推送。
//   - UserID/UserKey 为空：返回 nil，视作禁用推送（不报错，让上层简单写）
//   - Message 为空：同上
//   - 网络失败 / luckfast 返回非 code:0：返回 error
//
// 阻塞调用；上层决定要不要 goroutine 异步包一层。
func (c *Client) Send(ctx context.Context, opt Options) error {
	if opt.UserID == "" || opt.UserKey == "" {
		return nil
	}
	if opt.Message == "" {
		return nil
	}

	// 长度截断（APNs payload 总长度上限 4KB，luckfast 截图建议 < 500 字）
	if len(opt.Title) > 50 {
		opt.Title = opt.Title[:50]
	}
	if len(opt.Subtitle) > 50 {
		opt.Subtitle = opt.Subtitle[:50]
	}
	if len(opt.Message) > 500 {
		opt.Message = opt.Message[:500] + "..."
	}
	sound := opt.Sound
	if sound == "" {
		sound = "9"
	}

	// POST + application/x-www-form-urlencoded：避免 GET URL 超长（消息可能含中文 + URL 编码翻 3 倍）
	form := url.Values{}
	form.Set("title", opt.Title)
	if opt.Subtitle != "" {
		form.Set("subtitle", opt.Subtitle)
	}
	form.Set("message", opt.Message)
	if opt.JumpURL != "" {
		form.Set("url", opt.JumpURL)
	}
	form.Set("sound", sound)

	endpoint := fmt.Sprintf("%s/%s/%s", luckfastBaseURL,
		url.PathEscape(opt.UserID), url.PathEscape(opt.UserKey))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build push request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("push http error: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	bodyStr := strings.TrimSpace(string(body))

	if resp.StatusCode >= 400 {
		return fmt.Errorf("push http %d: %s", resp.StatusCode, bodyStr)
	}

	// luckfast 返回 JSON：{"code":0,"data":{...},"message":"成功"}
	// 简单字符串包含判断 "code":0，避免引入 JSON parse 开销
	if !strings.Contains(bodyStr, `"code":0`) && !strings.Contains(bodyStr, `"code": 0`) {
		return fmt.Errorf("push reject: %s", bodyStr)
	}

	if c.log != nil {
		preview := opt.Message
		if len(preview) > 60 {
			preview = preview[:60]
		}
		c.log.Info("push_sent",
			zap.String("uid", opt.UserID),
			zap.String("title", opt.Title),
			zap.String("preview", preview),
			zap.Int("status", resp.StatusCode))
	}
	return nil
}
