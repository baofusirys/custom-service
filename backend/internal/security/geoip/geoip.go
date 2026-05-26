// Package geoip 基于 ip2region xdb v2 的离线 IP → 地理位置解析。
//
// 设计目标：
//  1. 完全离线：xdb 文件随 Docker 镜像分发，不依赖任何外部 API。
//  2. 全内存索引：xdb 文件一次性 Load 到 []byte 常驻内存（~11MB），
//     查询走 vector index，毫秒级，零磁盘 IO。
//  3. 容错优先：xdb 文件不存在 / 解析失败 / IP 非法 → 返回 ("", "", "")，
//     绝不阻塞业务流程（VisitorSession 不能因为 geoip 挂了而 500）。
//  4. 线程安全：xdb.NewWithBuffer 返回的 Searcher 是只读的，多协程并发安全。
//
// ip2region.xdb 数据格式约定（v4.xdb，2025+ 仓库版本）：
//   - 单条记录格式："国家|省份|城市|ISP|国家代码"，5 段，| 分隔
//     例 "中国|河北省|石家庄市|联通|CN" / "United States|California|0|Google LLC|US"
//   - ⚠️ 注意：这跟旧 v2 xdb 的「国家|大区|省份|城市|ISP」格式段位完全不同！
//     旧 v2 的省份在 parts[2]、城市在 parts[3]；
//     新 v4 的省份在 parts[1]、城市在 parts[2]。混用会导致字段错位。
//   - 国内 IP 准确到地级市；国外 IP 通常省/市为 "0" 占位
//   - "0" 是占位符，表示该字段无数据（业务侧需要去掉）
package geoip

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
)

// Resolver 持有 xdb 全内存 searcher。
// 业务方拿一个全局单例即可（geoip.Default），无需自己 New。
type Resolver struct {
	searcher *xdb.Searcher
	disabled bool   // 加载失败时 true，所有 Lookup 直接返回空
	path     string // 加载来源路径，调试/日志用
}

// Result IP → 地理位置查询结果。所有字段都可能为空字符串。
//
// Country/Province/City 已经去除 ip2region 的 "0" 占位符（业务侧无需再处理）。
// ISP 暂不暴露给前端（运营商信息属于 PII 噪音，需要再开放）。
type Result struct {
	Country  string // 国家，如 "中国" / "美国"
	Province string // 省份，如 "广东省"
	City     string // 城市，如 "深圳市"
}

// Empty 返回 true 表示该 IP 没解析到任何地理信息（库未加载、IP 非法、
// 或者 xdb 里就是空记录如 "0|0|0|0|0"）。
func (r Result) Empty() bool {
	return r.Country == "" && r.Province == "" && r.City == ""
}

// New 用指定 xdb 路径构造 Resolver。
//   - path 文件不存在 → 返回 disabled=true 的 Resolver + error，业务侧可忽略 error 继续启动
//   - xdb 解析失败 → 同上
//   - 成功 → 全内存索引就绪
//
// 设计选择：失败不 panic、不阻塞启动。爷爷的部署铁律 1) 高可用 2) 模块独立失败不拖垮主流程。
func New(path string) (*Resolver, error) {
	r := &Resolver{path: path, disabled: true}
	if path == "" {
		return r, errors.New("geoip: empty xdb path")
	}
	buf, err := os.ReadFile(path)
	if err != nil {
		return r, fmt.Errorf("geoip: read xdb file %q: %w", path, err)
	}
	if len(buf) < 1024 {
		// xdb 至少有 256 字节 header + vector index，小于 1KB 必然是损坏的占位文件
		return r, fmt.Errorf("geoip: xdb file too small (%d bytes), likely corrupted", len(buf))
	}
	// v3.x API：NewWithBuffer 需要显式 Version 参数。
	// 从 xdb header 自动识别版本（Structure20 = 旧 v2 仅 IPv4 / Structure30 = 新 v3 区分 IPv4/IPv6），
	// 比硬编码 xdb.IPv4 鲁棒：换版本数据库不用改代码。
	if len(buf) < xdb.HeaderInfoLength {
		return r, fmt.Errorf("geoip: xdb buffer < HeaderInfoLength (%d)", xdb.HeaderInfoLength)
	}
	header, err := xdb.NewHeader(buf[:xdb.HeaderInfoLength])
	if err != nil {
		return r, fmt.Errorf("geoip: parse xdb header: %w", err)
	}
	version, err := xdb.VersionFromHeader(header)
	if err != nil {
		return r, fmt.Errorf("geoip: detect xdb version: %w", err)
	}
	searcher, err := xdb.NewWithBuffer(version, buf)
	if err != nil {
		return r, fmt.Errorf("geoip: init xdb searcher: %w", err)
	}
	r.searcher = searcher
	r.disabled = false
	return r, nil
}

// Lookup 把 IP 转成地理位置。所有失败路径都返回 Result{}（空结果），不返回 error。
//
// 业务侧调用示例：
//
//	geo := resolver.Lookup(clientIP)
//	v.Country = geo.Country
//	v.City = geo.City
//
// 即使 resolver 为 nil 或未加载成功，也安全（method on nil receiver 在 Go 里是合法的，
// 只要不解 nil 字段即可——这里我们提前 return 防御）。
func (r *Resolver) Lookup(ip string) Result {
	if r == nil || r.disabled || r.searcher == nil {
		return Result{}
	}
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return Result{}
	}
	// xdb 只支持 IPv4。IPv6 直接放过（返回空），不当作错误。
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		return Result{}
	}
	// v3.x API：Search(ip any) 接受 string / uint32 / net.IP 等多态参数。
	region, err := r.searcher.Search(ip)
	if err != nil || region == "" {
		return Result{}
	}
	// v4.xdb 返回格式："国家|省份|城市|ISP|国家代码"（5 段）
	// 例: "中国|河北省|石家庄市|联通|CN"  /  "United States|California|0|Google LLC|US"
	// "0" 是 ip2region 内部占位符，业务侧空字符串更友好
	parts := strings.Split(region, "|")
	res := Result{}
	if len(parts) >= 1 {
		res.Country = clean(parts[0])
	}
	if len(parts) >= 2 {
		res.Province = clean(parts[1])
	}
	if len(parts) >= 3 {
		res.City = clean(parts[2])
	}
	return res
}

// Disabled 返回 true 表示库未加载成功（生产环境用于上报监控）。
func (r *Resolver) Disabled() bool {
	return r == nil || r.disabled
}

// Path 返回加载来源（日志/排查用）。
func (r *Resolver) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

func clean(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return ""
	}
	return s
}

// ============ 全局单例（main 启动时初始化一次，handler 直接拿 Default()）============

var (
	globalOnce sync.Once
	global     *Resolver
)

// SetDefault 由 main 在启动时调用一次，把加载好的 Resolver 注入全局。
// 调用多次只第一次生效，保证幂等。
func SetDefault(r *Resolver) {
	globalOnce.Do(func() {
		global = r
	})
}

// Default 返回全局 Resolver。未 SetDefault 时返回 nil，
// nil Resolver 的 Lookup 也是安全的（返回 Result{}）。
func Default() *Resolver {
	return global
}
