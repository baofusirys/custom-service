package textfilter

import (
	"strings"
	"testing"
	"time"
)

func allOn() Config {
	return Config{Enabled: true, Mode: ModeMask, Categories: map[Category]bool{CatAd: true, CatLink: true, CatPorn: true, CatGambling: true, CatContact: true}}
}

func TestDetectsCategories(t *testing.T) {
	f := Compile(allOn())
	cases := []struct {
		in      string
		cat     Category
		mustNot string // 屏蔽后不得残留的片段
	}{
		{"加我微信 abc123456 有好东西", CatContact, "abc123456"},
		{"VX：Kefu_8888 私聊", CatContact, "kefu_8888"},
		{"ｖｘ ｈａｏ１２３４５", CatContact, "１２３４５"},
		{"qq 12345678", CatContact, "12345678"},
		{"扣扣：987654", CatContact, "987654"},
		{"电话 138 1234 5678 随时打", CatContact, "5678"},
		{"手机一三八一二三四五六七八", CatContact, "五六七八"},
		{"联系 13812345678", CatContact, "13812345678"},
		{"邮箱 spam.bot+1@gmail.com 联系", CatContact, "gmail"},
		{"tg 找 @spam_bot99", CatContact, "spam_bot99"},
		{"访问 https://evil.example.com/a?b=1 领取", CatLink, "evil"},
		{"上 www.spam-site.top 看看", CatLink, "spam-site"},
		{"打开 abc 点 com 有惊喜", CatLink, "com"},
		{"去 t.me/spamgroup 加入", CatLink, "spamgroup"},
		{"在線百 家 乐，日入过千", CatGambling, "家"},
		{"网赌上岸", CatGambling, "网赌"},
		{"同城约炮", CatPorn, "约炮"},
		{"诚招代理，扫码进群", CatAd, "代理"},
	}
	for _, c := range cases {
		r := f.Check(c.in)
		if !r.Filtered {
			t.Errorf("应命中：%q", c.in)
			continue
		}
		found := false
		for _, cat := range r.Categories {
			if cat == c.cat {
				found = true
			}
		}
		if !found {
			t.Errorf("%q 应含类别 %s，实际 %v", c.in, c.cat, r.Categories)
		}
		if strings.Contains(strings.ToLower(r.Masked), strings.ToLower(c.mustNot)) {
			t.Errorf("%q 屏蔽后仍残留 %q：%q", c.in, c.mustNot, r.Masked)
		}
		if !strings.Contains(r.Masked, MaskText) {
			t.Errorf("%q 屏蔽后应含 **：%q", c.in, r.Masked)
		}
	}
}

func TestNormalMessagesNotFiltered(t *testing.T) {
	f := Compile(allOn())
	for _, in := range []string{
		"可以用微信支付吗？",
		"你们收号吗，这个号多少钱",
		"我的订单号是 QA20479，什么时候发货",
		"价格是 1000 元，能便宜点吗",
		"这个账号上分很快，代练多久",
		"I have a question about the account",
		"请问客服在吗？谢谢",
		"版本 1.5.2 更新了吗",
		"skyline 这个游戏好玩",
		"今天是 2026-10-18",
	} {
		if r := f.Check(in); r.Filtered {
			t.Errorf("正常消息不应被屏蔽：%q -> %q %v", in, r.Masked, r.Hits)
		}
	}
}

func TestMaskMergeAndWholeMode(t *testing.T) {
	f := Compile(allOn())
	r := f.Check("加群 进群 请看")
	if r.Masked != "** ** 请看" && r.Masked != "** 请看" {
		t.Fatalf("相邻命中应分别或合并屏蔽：%q", r.Masked)
	}
	cfg := allOn()
	cfg.Mode = ModeWhole
	if r := Compile(cfg).Check("你好，加我微信 abc123456"); r.Masked != MaskText {
		t.Fatalf("整条模式应为 **：%q", r.Masked)
	}
}

func TestCategoriesSwitchCustomAndAllow(t *testing.T) {
	cfg := allOn()
	cfg.Categories[CatLink] = false
	cfg.CustomWords = []string{"竞品A", "  "}
	cfg.AllowWords = []string{"进群须知"}
	cfg.AllowDomains = []string{"xn--5kroo.com"}
	f := Compile(cfg)
	if r := f.Check("看 www.spam.top"); r.Filtered {
		t.Fatalf("关闭网址类别后不应命中：%v", r.Hits)
	}
	if r := f.Check("推荐 竞 品 A"); !r.Filtered || r.Categories[0] != CatCustom {
		t.Fatalf("自定义词应命中：%+v", r)
	}
	if r := f.Check("请先阅读进群须知"); r.Filtered {
		t.Fatalf("放行词范围内不应命中：%v", r.Hits)
	}
	cfg.Categories[CatLink] = true
	f = Compile(cfg)
	if r := f.Check("官网 https://xn--5kroo.com/item/1 和 www.xn--5kroo.com"); r.Filtered {
		t.Fatalf("放行域名不应命中：%v", r.Hits)
	}
	if r := f.Check("别去 https://xn--5kroo.com.evil.top"); !r.Filtered {
		t.Fatal("放行域名的后缀伪装必须命中")
	}
}

func TestDisabledAndEmpty(t *testing.T) {
	cfg := allOn()
	cfg.Enabled = false
	if r := Compile(cfg).Check("加我微信 abc123456"); r.Filtered || r.Masked != "加我微信 abc123456" {
		t.Fatal("关闭时原样返回")
	}
	if r := Compile(allOn()).Check(""); r.Filtered {
		t.Fatal("空内容不命中")
	}
}

func TestZeroWidthAndFullWidthEvasion(t *testing.T) {
	f := Compile(allOn())
	for _, in := range []string{"博\u200b彩", "Ｗｗｗ．ｓｐａｍ．ｃｏｍ", "约\u3000炮"} {
		if r := f.Check(in); !r.Filtered {
			t.Errorf("规避写法应命中：%q", in)
		}
	}
}

func TestNormalizeWord(t *testing.T) {
	if NormalizeWord(" 百 家，乐 ") != "百家乐" || NormalizeWord("ＡＢ c") != "abc" || NormalizeWord("，。") != "" {
		t.Fatal("归一化不符合预期")
	}
}

func TestPerformance(t *testing.T) {
	cfg := allOn()
	for i := 0; i < MaxCustomWords; i++ {
		cfg.CustomWords = append(cfg.CustomWords, "自定义词"+strings.Repeat("x", i%20)+string(rune('a'+i%26)))
	}
	f := Compile(cfg)
	text := strings.Repeat("你好请问这个账号多少钱，可以微信支付吗 ", 40)[:0] + strings.Repeat("客服你好abc 123 点评", 60)
	start := time.Now()
	for i := 0; i < 200; i++ {
		f.Check(text)
	}
	if per := time.Since(start) / 200; per > 5*time.Millisecond {
		t.Fatalf("单条检测过慢：%v", per)
	}
}
