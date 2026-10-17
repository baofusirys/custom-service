package textfilter

// 内置词库（[122] 大厅敏感词屏蔽）。
// 原则：只收「明确的垃圾/违规」说法，避开本站正常业务用语（如「收号」「出号」「代练」「上分」），
// 误杀可在后台用「放行词」放行，漏网可用「自定义屏蔽词」补充。所有词在匹配前与正文一样做归一化
// （全角转半角、小写、去掉字间空格与标点），所以这里不需要再写带空格或全角的变体。

var builtinAd = []string{
	"招代理", "诚招代理", "招募代理", "招收代理", "代理招募", "日赚", "日入过千", "日入上千", "月入过万", "月入上万", "躺赚",
	"稳赚不赔", "包赚", "带你赚钱", "网赚", "兼职刷单", "刷单", "刷信誉", "引流", "加粉", "涨粉", "精准粉",
	"广告合作", "推广合作", "免费领取", "扫码领取", "扫码进群", "扫码加群", "进群", "加群", "拉群", "群号",
	"私聊有惊喜", "撸羊毛", "薅羊毛", "投资理财", "理财群", "炒币", "带单", "喊单", "跑分", "代收款", "洗钱",
}

var builtinPorn = []string{
	"约炮", "裸聊", "色情", "黄片", "黄网", "成人网站", "成人视频", "一夜情", "援交", "包养", "上门服务", "小姐服务",
	"楼凤", "迷药", "催情", "春药", "av女优", "无码", "露点", "裸照", "性服务", "做爱", "嫖娼", "卖淫", "色播",
	"黄播", "福利姬", "同城约", "同城交友", "约p", "看片", "毛片", "三级片", "a片", "情色", "裸体", "性交",
}

var builtinGambling = []string{
	"博彩", "赌博", "网赌", "赌场", "百家乐", "时时彩", "六合彩", "北京赛车", "幸运飞艇", "老虎机", "真人娱乐", "菠菜",
	"体育投注", "彩票代理", "赌球", "赌钱", "澳门赌场", "新葡京", "威尼斯人", "太阳城", "皇冠体育", "开元棋牌",
	"炸金花", "首充送", "充值送彩金", "彩金", "返水", "打码量", "庄家", "下注", "投注", "盘口", "外围赌",
}

// contactKeywords 联系方式关键词：单独出现不屏蔽，只有后面跟着账号/号码时才算联系方式（见 contactRe）。
var contactKeywords = []string{
	"微信", "微信号", "薇信", "威信", "徽信", "v信", "vx", "wx", "weixin", "wechat", "加v", "+v", "绿泡泡",
	"qq", "qq号", "扣扣", "企鹅", "企鹅号", "tg", "telegram", "飞机", "飞机号", "纸飞机", "电报", "whatsapp",
	"skype", "line", "电话", "手机", "手机号", "联系方式", "tel",
}

// linkTLDs 识别裸域名用的顶级域（不含中文顶级域；带协议或 www. 的网址不受此表限制）。
var linkTLDs = []string{
	"com", "cn", "net", "org", "top", "xyz", "vip", "cc", "me", "io", "co", "info", "biz", "club", "site", "online",
	"shop", "store", "app", "link", "live", "tv", "asia", "ink", "wang", "work", "fun", "icu", "life", "pro", "today",
	"win", "bet", "game", "games", "ltd", "cloud", "tech", "space", "website", "ren", "pub", "mobi", "name", "hk",
	"tw", "jp", "kr", "us", "uk", "ru", "so", "la", "ws", "gg", "to", "ly", "su", "im",
}

// BuiltinWords 内置词库副本（后台只读展示）。
func BuiltinWords() map[string][]string {
	cp := func(s []string) []string { return append([]string(nil), s...) }
	return map[string][]string{
		"ad": cp(builtinAd), "porn": cp(builtinPorn), "gambling": cp(builtinGambling), "contact_keywords": cp(contactKeywords),
	}
}
