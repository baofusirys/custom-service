import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const html = readFileSync(new URL('../public/chat.html', import.meta.url), 'utf8')
const scripts = [...html.matchAll(/<script[^>]*>([\s\S]*?)<\/script>/g)]
const inlineScript = scripts.at(-1)?.[1] || ''

test('Widget 内联脚本语法有效', () => {
  assert.doesNotThrow(() => new Function(inlineScript))
})

test('Widget 使用密码学消息 ID，不再使用 Date.now 作为幂等键', () => {
  assert.match(inlineScript, /crypto\.getRandomValues/)
  assert.match(inlineScript, /return 'm2-' \+ hex/)
  assert.doesNotMatch(inlineScript, /id:\s*['"]local-['"]\s*\+\s*Date\.now/)
})

test('客服消息渲染后回 delivery，打开后回带消息 ID 的 read', () => {
  assert.match(inlineScript, /type:\s*'delivery'/)
  assert.match(inlineScript, /sendDeliveryAck\(m\.id\)/)
  assert.match(inlineScript, /sendReadAck\(m\.id\)/)
})

test('离线重放消息按消息 ID 去重但仍重复确认送达', () => {
  assert.match(inlineScript, /seenMessageIDs\[m\.id\]/)
  const dedupeAt = inlineScript.indexOf('var duplicate =')
  const deliveryAt = inlineScript.indexOf('sendDeliveryAck(m.id)', dedupeAt)
  assert.ok(dedupeAt >= 0 && deliveryAt > dedupeAt)
  assert.match(inlineScript, /if \(!duplicate && !isWidgetOpen\)/)
})
