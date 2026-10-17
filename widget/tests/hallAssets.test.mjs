import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import vm from 'node:vm'

// [119] 大厅相关静态资源与配置：入口脚本顺序、第三方文件完整性、CSP / 缓存头、无 Emoji
const pub = (p) => new URL('../public/' + p, import.meta.url)
const html = readFileSync(pub('chat.html'), 'utf8')
const hallJs = readFileSync(pub('hall.js'), 'utf8')
const nginx = readFileSync(new URL('../nginx.conf', import.meta.url), 'utf8')
const dockerfile = readFileSync(new URL('../Dockerfile', import.meta.url), 'utf8')
const VENDOR = 'vendor/livekit-client-2.23.0.umd.js'

test('chat.html：大厅脚本先于 chat.js、全部 defer 外链、相对路径、无内联脚本', () => {
  const scripts = [...html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script>/g)]
  assert.deepEqual(scripts.map(m => /src="([^"]+)"/.exec(m[1])[1]), ['hall-core.js', 'hall.js', '/chat.js'])
  for (const m of scripts) {
    assert.match(m[1], /\bdefer\b/)
    assert.equal(m[2].trim(), '', '不允许内联脚本')
  }
  assert.doesNotMatch(html, /\son[a-z]+="/i, '不允许内联事件处理器（CSP script-src self）')
  assert.doesNotMatch(html, new RegExp(VENDOR.replace(/[.]/g, '\\.')), 'livekit-client 由 hall.js 空闲时按需加载，不在入口同步引入')
})

test('chat.html：页签、大厅页与私聊页结构，隐私提示与默认关麦', () => {
  for (const id of ['tabs', 'tabHall', 'tabPrivate', 'tabPrivateDot', 'hallPane', 'privatePane', 'hallPeople', 'hallVideo', 'hallMsgs', 'hallInput', 'hallSendBtn', 'hallFileInput', 'hallMicBtn', 'hallCamBtn', 'hallMuteBtn', 'hallNotice', 'hallAudioSink', 'list', 'input', 'sendBtn', 'voiceBtn']) {
    assert.match(html, new RegExp('id="' + id + '"'), id)
  }
  assert.match(html, /大厅消息所有人可见，订单号、账号等隐私请到/)
  assert.match(html, /id="hallMicBtn"[^>]*aria-pressed="false"/)
  assert.match(html, /id="hallCamBtn"[^>]*hidden/)
  assert.match(html, /id="hallInput"[^>]*maxlength="1000"/)
  // [123] 客服暂停文字聊天 / 语音时的提示：默认隐藏、文案固定、使用 SVG 图标；输入框与待发送附件在同一容器内
  for (const id of ['hallInputWrap', 'hallChatPaused', 'hallChatPausedText', 'hallVoicePaused', 'hallVoicePausedText']) assert.match(html, new RegExp('id="' + id + '"'), id)
  assert.match(html, /id="hallChatPaused"[^>]*hidden/)
  assert.match(html, /id="hallVoicePaused"[^>]*hidden/)
  assert.match(html, /id="hallChatPausedText">大厅文字聊天已暂停</)
  assert.match(html, /id="hallVoicePausedText">大厅语音已暂停</)
  const wrap = html.slice(html.indexOf('id="hallInputWrap"'))
  assert.ok(wrap.indexOf('id="hallPendingList"') < wrap.indexOf('id="hallInput"') && wrap.indexOf('id="hallInput"') < wrap.indexOf('</div>', wrap.indexOf('id="hallInput"')))
  // 私聊原有节点仍在私聊页内
  const priv = html.slice(html.indexOf('id="privatePane"'))
  assert.ok(priv.indexOf('id="list"') > 0 && priv.indexOf('id="sendBtn"') > 0)
})

test('vendor：livekit-client 文件与 SHA512SUMS、hall.js 的 SRI 一致，许可证随附', () => {
  const buf = readFileSync(pub(VENDOR))
  const sums = readFileSync(pub('vendor/SHA512SUMS'), 'utf8').trim().split(/\s+/)
  assert.equal(sums[1], 'livekit-client-2.23.0.umd.js')
  assert.equal(createHash('sha512').update(buf).digest('hex'), sums[0])
  const sri = /var LK_SRI = '(sha384-[A-Za-z0-9+/=]+)'/.exec(hallJs)
  assert.ok(sri, 'hall.js 缺少 LK_SRI')
  assert.equal('sha384-' + createHash('sha384').update(buf).digest('base64'), sri[1])
  assert.match(hallJs, /var LK_SRC = 'vendor\/livekit-client-2\.23\.0\.umd\.js'/)
  const lic = readFileSync(pub('vendor/livekit-client-2.23.0.LICENSE.txt'), 'utf8')
  assert.match(lic, /Apache License/)
  assert.match(lic, /Version 2\.0/)
  const notices = readFileSync(pub('vendor/THIRD_PARTY_NOTICES.txt'), 'utf8')
  assert.match(notices, /livekit-client 2\.23\.0/)
  assert.match(notices, /sha512-j9Kg86nJjBjNFUD\+ukp2jwMCGa0k\+YD5gU\/\/RzAM\+ZY3pbG631QCte7fQjwqWMmtBzhDJL9nKYuOLMv\/9IbAxg==/)
  // UMD 文件可解析、导出 LivekitClient 全局且不使用 eval / new Function / Worker（CSP 兼容）
  const code = buf.toString('utf8')
  assert.doesNotThrow(() => new vm.Script(code, { filename: 'livekit.umd.js' }))
  assert.match(code, /\.LivekitClient=\{\}/)
  assert.doesNotMatch(code, /\beval\(|new Function\(|new Worker\(/)
})

test('Dockerfile：构建时校验 vendor 哈希、固定基础镜像版本', () => {
  assert.match(dockerfile, /sha512sum -c SHA512SUMS/)
  assert.match(dockerfile, /^FROM nginx:\d+\.\d+\.\d+-alpine\d+\.\d+$/m)
  assert.doesNotMatch(dockerfile, /:latest/)
})

test('nginx.conf：chat.html CSP 放行同源 wss 与 blob 媒体、禁止内联脚本；vendor 长缓存；业务脚本每次验证', () => {
  const chat = /location = \/chat\.html \{([\s\S]*?)\n  \}/.exec(nginx)[1]
  const csp = /Content-Security-Policy "([^"]+)"/.exec(chat)[1]
  const dir = Object.fromEntries(csp.split(';').map(s => s.trim()).filter(Boolean).map(s => { const [k, ...v] = s.split(/\s+/); return [k, v] }))
  assert.deepEqual(dir['script-src'], ["'self'"])
  assert.deepEqual(dir['connect-src'], ["'self'", 'wss://$host'])
  assert.ok(dir['media-src'].includes('blob:') && dir['media-src'].includes("'self'"))
  assert.deepEqual(dir['object-src'], ["'none'"])
  assert.deepEqual(dir['frame-ancestors'], ['*'])
  assert.ok(!csp.includes("'unsafe-eval'") && !/script-src[^;]*\*/.test(csp))
  assert.match(chat, /X-Content-Type-Options "nosniff"/, 'location 内写了 add_header 后必须重新声明 nosniff')
  const vendor = /location \^~ \/vendor\/ \{([\s\S]*?)\n  \}/.exec(nginx)[1]
  assert.match(vendor, /max-age=31536000, immutable/)
  assert.match(vendor, /nosniff/)
  assert.match(nginx, /location ~ \^\/\(chat\|hall\|hall-core\)\\\.js\$ \{[\s\S]*?no-cache, must-revalidate/)
  assert.match(nginx, /server_tokens off;/)
})

// 前端界面严禁 Emoji：扫描访客窗口运行时文件（入口、挂件脚本与大厅模块）
test('挂件运行时前端文件不含 Emoji', () => {
  const emoji = /\p{Extended_Pictographic}|\u{FE0F}|[\u{1F1E6}-\u{1F1FF}]/u
  const files = ['chat.html', 'chat.js', 'hall.js', 'hall-core.js', 'loader.js'].map(pub)
  for (const f of files) {
    const text = readFileSync(f, 'utf8')
    const m = emoji.exec(text)
    assert.equal(m, null, f.pathname + ' 含 Emoji：' + (m && m[0]))
  }
})
