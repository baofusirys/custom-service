package textfilter

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// [122] 大厅文字敏感内容识别与屏蔽（纯计算，无 IO，可并发调用）。
//
// 识别对象：广告、超链接/网址、色情、博彩、联系方式（手机号、长数字、关键词+账号、邮箱、@用户名）、自定义词。
// 归一化：全角转半角、小写；关键词在「紧凑视图」（去掉空白、标点、符号、零宽字符）上匹配，
// 因此「百 家 乐」「v.x」「Ｖ Ｘ」都能识别；网址与联系方式在等长「链接视图」上用正则识别，位置可直接映射回原文。
// 屏蔽：命中区间合并后替换为 "**"（ModeMask），或整条替换为 "**"（ModeWhole）。放行词覆盖范围内的命中不屏蔽。

// Category 命中类别。
type Category string

const (
	CatAd       Category = "ad"
	CatLink     Category = "link"
	CatPorn     Category = "porn"
	CatGambling Category = "gambling"
	CatContact  Category = "contact"
	CatCustom   Category = "custom"
)

// AllCategories 可在后台开关的内置类别（自定义词始终生效）。
var AllCategories = []Category{CatAd, CatLink, CatPorn, CatGambling, CatContact}

// Mode 屏蔽方式。
type Mode string

const (
	ModeMask  Mode = "mask"
	ModeWhole Mode = "whole"
)

// MaskText 屏蔽后的替换文本。
const MaskText = "**"

// 后台配置上限（与契约 9.2 limits 一致）。
const (
	MaxCustomWords  = 500
	MaxAllowWords   = 200
	MaxAllowDomains = 50
	MaxWordRunes    = 32
)

// Config 过滤配置（已校验）。
type Config struct {
	Enabled      bool
	Mode         Mode
	Categories   map[Category]bool
	CustomWords  []string
	AllowWords   []string
	AllowDomains []string // 小写 ASCII（中文域名已转 punycode）
}

// Hit 一处命中（原文 rune 下标，左闭右开）。
type Hit struct {
	Category Category
	Start    int
	End      int
}

// HitText 命中的原文片段（后台「检测试用」展示）。
type HitText struct {
	Category Category `json:"category"`
	Text     string   `json:"text"`
}

// Result 检测结果。
type Result struct {
	Filtered   bool
	Masked     string
	Categories []Category
	Hits       []HitText
}

// Filter 已编译的过滤器（只读，可并发使用）。
type Filter struct {
	cfg      Config
	keywords *automaton
	allow    *automaton
	domains  []string
}

// ErrEmptyWord 归一化后为空的词（只有标点/空白）。
var ErrEmptyWord = errors.New("textfilter: empty word after normalization")

// NormalizeWord 词的归一化形式（与紧凑视图一致）；用于后台去重与校验。
func NormalizeWord(w string) string {
	var b strings.Builder
	for _, r := range w {
		if n, ok := compactRune(r); ok {
			b.WriteRune(n)
		}
	}
	return b.String()
}

// Compile 编译配置。词表中归一化为空的词会被忽略（后台保存时已拒绝）。
func Compile(cfg Config) *Filter {
	f := &Filter{cfg: cfg}
	if cfg.Mode != ModeWhole {
		f.cfg.Mode = ModeMask
	}
	var words [][]rune
	var meta []acPattern
	add := func(list []string, cat Category) {
		for _, w := range list {
			n := []rune(NormalizeWord(w))
			if len(n) == 0 {
				continue
			}
			words = append(words, n)
			meta = append(meta, acPattern{length: len(n), cat: cat, ascii: isASCIIWord(n)})
		}
	}
	if cfg.Categories[CatAd] {
		add(builtinAd, CatAd)
	}
	if cfg.Categories[CatPorn] {
		add(builtinPorn, CatPorn)
	}
	if cfg.Categories[CatGambling] {
		add(builtinGambling, CatGambling)
	}
	add(cfg.CustomWords, CatCustom)
	f.keywords = buildAutomaton(words, meta)
	var allowWords [][]rune
	var allowMeta []acPattern
	for _, w := range cfg.AllowWords {
		n := []rune(NormalizeWord(w))
		if len(n) == 0 {
			continue
		}
		allowWords = append(allowWords, n)
		allowMeta = append(allowMeta, acPattern{length: len(n), ascii: isASCIIWord(n)})
	}
	f.allow = buildAutomaton(allowWords, allowMeta)
	for _, d := range cfg.AllowDomains {
		d = strings.Trim(strings.ToLower(strings.TrimSpace(d)), ".")
		if d != "" {
			f.domains = append(f.domains, d)
		}
	}
	return f
}

// Config 返回编译时使用的配置。
func (f *Filter) Config() Config { return f.cfg }

// Check 检测并生成屏蔽结果。未启用时原样返回。
func (f *Filter) Check(text string) Result {
	res := Result{Masked: text}
	if f == nil || !f.cfg.Enabled || text == "" {
		return res
	}
	orig := []rune(text)
	v := buildViews(orig)
	var hits []Hit
	// 1) 关键词（广告/色情/博彩/自定义）
	f.keywords.scan(v.compact, func(p acPattern, end int) {
		start := end - p.length + 1
		if p.ascii && !asciiBoundary(v.compact, start, end) {
			return
		}
		hits = append(hits, Hit{Category: p.cat, Start: v.compactIdx[start], End: v.compactIdx[end] + 1})
	})
	// 2) 网址
	if f.cfg.Categories[CatLink] {
		hits = append(hits, f.linkHits(v)...)
	}
	// 3) 联系方式
	if f.cfg.Categories[CatContact] {
		hits = append(hits, contactHits(v)...)
	}
	if len(hits) == 0 {
		return res
	}
	// 放行词：命中完全落在放行词范围内则忽略
	if f.allow != nil && len(f.allow.pats) > 0 {
		var allowSpans [][2]int
		f.allow.scan(v.compact, func(p acPattern, end int) {
			start := end - p.length + 1
			if p.ascii && !asciiBoundary(v.compact, start, end) {
				return
			}
			allowSpans = append(allowSpans, [2]int{v.compactIdx[start], v.compactIdx[end] + 1})
		})
		if len(allowSpans) > 0 {
			kept := hits[:0]
			for _, h := range hits {
				covered := false
				for _, a := range allowSpans {
					if h.Start >= a[0] && h.End <= a[1] {
						covered = true
						break
					}
				}
				if !covered {
					kept = append(kept, h)
				}
			}
			hits = kept
		}
	}
	if len(hits) == 0 {
		return res
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Start != hits[j].Start {
			return hits[i].Start < hits[j].Start
		}
		return hits[i].End > hits[j].End
	})
	seenCat := map[Category]bool{}
	for _, h := range hits {
		if !seenCat[h.Category] {
			seenCat[h.Category] = true
			res.Categories = append(res.Categories, h.Category)
		}
		res.Hits = append(res.Hits, HitText{Category: h.Category, Text: string(orig[h.Start:h.End])})
	}
	sort.Slice(res.Categories, func(i, j int) bool { return categoryOrder(res.Categories[i]) < categoryOrder(res.Categories[j]) })
	res.Filtered = true
	if f.cfg.Mode == ModeWhole {
		res.Masked = MaskText
		return res
	}
	// 合并重叠/相邻区间后替换
	var merged [][2]int
	for _, h := range hits {
		if n := len(merged); n > 0 && h.Start <= merged[n-1][1] {
			if h.End > merged[n-1][1] {
				merged[n-1][1] = h.End
			}
			continue
		}
		merged = append(merged, [2]int{h.Start, h.End})
	}
	var b strings.Builder
	b.Grow(len(text))
	last := 0
	for _, m := range merged {
		b.WriteString(string(orig[last:m[0]]))
		b.WriteString(MaskText)
		last = m[1]
	}
	b.WriteString(string(orig[last:]))
	res.Masked = b.String()
	return res
}

func categoryOrder(c Category) int {
	switch c {
	case CatAd:
		return 0
	case CatLink:
		return 1
	case CatPorn:
		return 2
	case CatGambling:
		return 3
	case CatContact:
		return 4
	default:
		return 5
	}
}

// ============ 归一化与视图 ============

// foldRune 全角转半角、大小写统一、句号/中文点号转 "."（等长映射）。
func foldRune(r rune) rune {
	switch {
	case r >= 0xFF01 && r <= 0xFF5E:
		r -= 0xFEE0
	case r == 0x3000:
		r = ' '
	case r == 0x3002 || r == 0xFF61 || r == 0x2024 || r == 0xFE52: // 。 ｡ ․ ﹒
		r = '.'
	}
	return unicode.ToLower(r)
}

// compactRune 紧凑视图：只保留字母、数字（含汉字），其余（空白、标点、符号、零宽、表情）丢弃。
func compactRune(r rune) (rune, bool) {
	r = foldRune(r)
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return r, true
	}
	return 0, false
}

type views struct {
	orig       []rune
	compact    []rune
	compactIdx []int  // compact 下标 -> 原文下标
	link       string // 与原文等长（按 rune）的链接视图
	linkByte   []int  // link 的字节偏移 -> rune 下标
}

func buildViews(orig []rune) *views {
	v := &views{orig: orig, compact: make([]rune, 0, len(orig)), compactIdx: make([]int, 0, len(orig))}
	link := make([]rune, len(orig))
	for i, r := range orig {
		if c, ok := compactRune(r); ok {
			v.compact = append(v.compact, c)
			v.compactIdx = append(v.compactIdx, i)
		}
		fr := foldRune(r)
		if unicode.Is(unicode.Cf, r) { // 零宽字符在链接视图里当空格，避免把网址拆开后漏检
			fr = ' '
		}
		link[i] = fr
	}
	// 「点」夹在字母数字之间时视为 "."（如 abc点com）
	for i, r := range link {
		if r != '点' {
			continue
		}
		p, n := i-1, i+1
		for p >= 0 && link[p] == ' ' {
			p--
		}
		for n < len(link) && link[n] == ' ' {
			n++
		}
		if p >= 0 && n < len(link) && isASCIIAlnum(link[p]) && isASCIILetter(link[n]) {
			link[i] = '.'
		}
	}
	v.link = string(link)
	v.linkByte = make([]int, len(v.link)+1)
	idx := 0
	for b := range v.link {
		v.linkByte[b] = idx
		idx++
	}
	v.linkByte[len(v.link)] = idx
	return v
}

func (v *views) runeSpan(byteStart, byteEnd int) (int, int) {
	return v.linkByte[byteStart], v.linkByte[byteEnd]
}

func isASCIIAlnum(r rune) bool {
	return r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r))
}
func isASCIILetter(r rune) bool { return r < utf8.RuneSelf && unicode.IsLetter(r) }

func isASCIIWord(w []rune) bool {
	for _, r := range w {
		if r >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// asciiBoundary 纯 ASCII 词两侧不能紧贴 ASCII 字母数字（避免 "have" 命中 "av"）。
func asciiBoundary(c []rune, start, end int) bool {
	if start > 0 && isASCIIAlnum(c[start-1]) {
		return false
	}
	if end+1 < len(c) && isASCIIAlnum(c[end+1]) {
		return false
	}
	return true
}

// ============ 网址 ============

var (
	urlRe    = regexp.MustCompile(`(?:https?|ftp)://[^\s<>"'，。、；！？（）\[\]]+|www\s?\.\s?[a-z0-9][a-z0-9-]*(?:\s?\.\s?[a-z0-9][a-z0-9-]*)+(?:[/?#][^\s<>"'，。、；！？（）\[\]]*)?`)
	domainRe = regexp.MustCompile(`[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\s?\.\s?[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*\s?\.\s?(?:` + strings.Join(linkTLDs, "|") + `)(?:\.(?:cn|hk|tw|uk|jp))?(?::[0-9]{1,5})?(?:/[^\s<>"'，。、；！？（）\[\]]*)?`)
	emailRe  = regexp.MustCompile(`[a-z0-9._%+-]{1,64}@[a-z0-9-]+(?:\.[a-z0-9-]+)*\.[a-z]{2,24}`)
	handleRe = regexp.MustCompile(`@[a-z][a-z0-9_]{4,31}`)
)

func (f *Filter) linkHits(v *views) []Hit {
	var hits []Hit
	taken := func(s, e int) bool {
		for _, h := range hits {
			if s >= h.Start && e <= h.End {
				return true
			}
		}
		return false
	}
	for _, m := range urlRe.FindAllStringIndex(v.link, -1) {
		s, e := v.runeSpan(m[0], m[1])
		if f.allowedHost(hostOf(v.link[m[0]:m[1]])) {
			continue
		}
		hits = append(hits, Hit{Category: CatLink, Start: s, End: e})
	}
	for _, m := range domainRe.FindAllStringIndex(v.link, -1) {
		s, e := v.runeSpan(m[0], m[1])
		if taken(s, e) {
			continue
		}
		// 左边界：不能紧贴字母数字、@、点、连字符（属于更长的词或邮箱，邮箱由联系方式规则处理）
		if s > 0 {
			p := []rune(v.link)[s-1]
			if isASCIIAlnum(p) || p == '@' || p == '.' || p == '-' || p == '_' {
				continue
			}
		}
		if f.allowedHost(hostOf(v.link[m[0]:m[1]])) {
			continue
		}
		hits = append(hits, Hit{Category: CatLink, Start: s, End: e})
	}
	return hits
}

// hostOf 从命中的网址片段取主机名（去协议、端口、路径与空白）。
func hostOf(u string) string {
	u = strings.ReplaceAll(u, " ", "")
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexAny(u, "/?#"); i >= 0 {
		u = u[:i]
	}
	if i := strings.LastIndex(u, "@"); i >= 0 {
		u = u[i+1:]
	}
	if i := strings.Index(u, ":"); i >= 0 {
		u = u[:i]
	}
	return strings.Trim(u, ".")
}

func (f *Filter) allowedHost(host string) bool {
	if host == "" {
		return false
	}
	for _, d := range f.domains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// ============ 联系方式 ============

var contactRe = regexp.MustCompile(`(?:微信号?|薇信|威信|徽信|v信|vx|wx|weixin|wechat|加v|\+v|绿泡泡|qq号?|扣扣|企鹅号?|tg|telegram|飞机号?|纸飞机|电报|whatsapp|skype|line|电话|手机号?|联系方式|tel)\s{0,2}(?:号码|号|账号|id)?\s{0,2}[:=是为]?\s{0,2}([a-z0-9][-_a-z0-9]{4,29})`)

// contactKeywordRunes 数字串前的联系方式提示词（5~8 位数字只有跟在这些词后才算联系方式）。
var contactHintRe = regexp.MustCompile(`(?:qq|扣扣|企鹅|微信|vx|wx|v信|电话|手机|tel|联系|号码|加我|whatsapp|tg)\s{0,2}(?:号码|号|账号)?\s{0,2}[:=是为]?\s{0,2}$`)

func contactHits(v *views) []Hit {
	var hits []Hit
	runes := []rune(v.link)
	for _, m := range contactRe.FindAllStringSubmatchIndex(v.link, -1) {
		s, e := v.runeSpan(m[0], m[1])
		// 关键词左侧不能紧贴 ASCII 字母数字（避免 "vxworks"、"skyline" 之类）
		if s > 0 && isASCIIAlnum(runes[s-1]) {
			continue
		}
		hits = append(hits, Hit{Category: CatContact, Start: s, End: e})
	}
	for _, m := range emailRe.FindAllStringIndex(v.link, -1) {
		s, e := v.runeSpan(m[0], m[1])
		hits = append(hits, Hit{Category: CatContact, Start: s, End: e})
	}
	for _, m := range handleRe.FindAllStringIndex(v.link, -1) {
		s, e := v.runeSpan(m[0], m[1])
		if s > 0 && (isASCIIAlnum(runes[s-1]) || runes[s-1] == '.') {
			continue // 邮箱的一部分
		}
		hits = append(hits, Hit{Category: CatContact, Start: s, End: e})
	}
	hits = append(hits, digitRunHits(v, runes)...)
	return hits
}

// digitValue 数字识别：ASCII/全角数字、中文与大写数字、圈号数字。
func digitValue(r rune) (byte, bool) {
	r = foldRune(r)
	if r >= '0' && r <= '9' {
		return byte(r), true
	}
	switch r {
	case '零', '〇', '⓪':
		return '0', true
	case '一', '壹', '幺', '①':
		return '1', true
	case '二', '贰', '②':
		return '2', true
	case '三', '叁', '③':
		return '3', true
	case '四', '肆', '④':
		return '4', true
	case '五', '伍', '⑤':
		return '5', true
	case '六', '陆', '⑥':
		return '6', true
	case '七', '柒', '⑦':
		return '7', true
	case '八', '捌', '⑧':
		return '8', true
	case '九', '玖', '⑨':
		return '9', true
	}
	return 0, false
}

func isDigitSeparator(r rune) bool {
	switch r {
	case ' ', '-', '_', '.', '·', '、', ',', '，', '/', '|', '	', '—', '－', '~', '*':
		return true
	}
	return false
}

var mobileRe = regexp.MustCompile(`1[3-9][0-9]{9}`)

// digitRunHits 连续数字串（允许中间夹最多 2 个分隔符）：含 11 位手机号、或 ≥9 位，或 5~8 位且前面有联系方式提示词。
func digitRunHits(v *views, link []rune) []Hit {
	var hits []Hit
	n := len(v.orig)
	for i := 0; i < n; {
		if _, ok := digitValue(v.orig[i]); !ok {
			i++
			continue
		}
		// ASCII 数字紧贴在 ASCII 字母后（如 abc123456 账号）由 contactRe 处理；这里仍按数字串识别
		start, end := i, i
		var digits []byte
		j := i
		for j < n {
			if d, ok := digitValue(v.orig[j]); ok {
				digits = append(digits, d)
				end = j + 1
				j++
				continue
			}
			k := j
			for k < n && k-j < 2 && isDigitSeparator(foldRune(v.orig[k])) {
				k++
			}
			if k > j && k < n {
				if _, ok := digitValue(v.orig[k]); ok {
					j = k
					continue
				}
			}
			break
		}
		i = end
		if end == start {
			i++
			continue
		}
		cnt := len(digits)
		switch {
		case cnt >= 11 && mobileRe.Match(digits):
		case cnt >= 9:
		case cnt >= 5:
			prefixStart := start - 12
			if prefixStart < 0 {
				prefixStart = 0
			}
			if !contactHintRe.MatchString(string(link[prefixStart:start])) {
				continue
			}
		default:
			continue
		}
		hits = append(hits, Hit{Category: CatContact, Start: start, End: end})
	}
	return hits
}
