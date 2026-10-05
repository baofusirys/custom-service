import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const html = readFileSync(new URL('../public/chat.html', import.meta.url), 'utf8')
const scriptSource = readFileSync(new URL('../public/chat.js', import.meta.url), 'utf8')

test('Widget 外部脚本语法有效且入口不含内联脚本', () => {
  assert.doesNotThrow(() => new Function(scriptSource))
  assert.match(html, /<script\s+src="\/chat\.js"\s+defer><\/script>/)
  assert.doesNotMatch(html, /<script>\s*[\s\S]+<\/script>/)
})

test('Widget 使用密码学消息 ID，不再使用 Date.now 作为幂等键', () => {
  assert.match(scriptSource, /crypto\.getRandomValues/)
  assert.match(scriptSource, /return 'm2-' \+ hex/)
  assert.doesNotMatch(scriptSource, /id:\s*['"]local-['"]\s*\+\s*Date\.now/)
})

test('客服消息渲染后回 delivery，打开后回带消息 ID 的 read', () => {
  assert.match(scriptSource, /type:\s*'delivery'/)
  assert.match(scriptSource, /sendDeliveryAck\(m\.id\)/)
  assert.match(scriptSource, /sendReadAck\(m\.id\)/)
})

test('离线重放消息按消息 ID 去重但仍重复确认送达', () => {
  assert.match(scriptSource, /seenMessageIDs\[m\.id\]/)
  const dedupeAt = scriptSource.indexOf('var duplicate =')
  const deliveryAt = scriptSource.indexOf('sendDeliveryAck(m.id)', dedupeAt)
  assert.ok(dedupeAt >= 0 && deliveryAt > dedupeAt)
  assert.match(scriptSource, /if \(!duplicate && !isWidgetOpen\)/)
})
