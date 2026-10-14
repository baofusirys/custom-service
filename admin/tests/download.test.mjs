import test from 'node:test'
import assert from 'node:assert/strict'

import {
  exportFallbackName,
  normalizeExportFormat,
  parseContentDispositionFilename,
  sanitizeFilename,
} from '../src/modules/download.js'

test('文件名优先取 filename*（UTF-8 百分号编码），其次 filename', () => {
  const header = "attachment; filename=\"fallback.xlsx\"; filename*=UTF-8''%E8%81%8A%E5%A4%A9%E8%AE%B0%E5%BD%95-20261014.xlsx"
  assert.equal(parseContentDispositionFilename(header), '聊天记录-20261014.xlsx')
  assert.equal(parseContentDispositionFilename('attachment; filename="a;b.txt"'), 'a;b.txt')
  assert.equal(parseContentDispositionFilename('attachment; filename=plain.txt'), 'plain.txt')
  assert.equal(parseContentDispositionFilename("attachment; filename*=\"utf-8'zh-cn'%E5%AF%BC%E5%87%BA.txt\""), '导出.txt')
  assert.equal(parseContentDispositionFilename("attachment; filename*=iso-8859-1''caf%E9.txt"), 'café.txt')
})

test('filename* 编码错误时回退到 filename；缺失或异常头部返回空串', () => {
  assert.equal(parseContentDispositionFilename("attachment; filename*=UTF-8''%E0%A4%A; filename=\"ok.xlsx\""), 'ok.xlsx')
  assert.equal(parseContentDispositionFilename('attachment'), '')
  assert.equal(parseContentDispositionFilename(''), '')
  assert.equal(parseContentDispositionFilename(null), '')
  assert.equal(parseContentDispositionFilename('attachment; filename=' + 'a'.repeat(5000)), '')
})

test('文件名安全化：去路径穿越、控制字符、保留字符与双向文本伪装', () => {
  assert.equal(sanitizeFilename('../../etc/passwd'), 'passwd')
  assert.equal(sanitizeFilename('..\\..\\boot.ini'), 'boot.ini')
  assert.equal(sanitizeFilename('a<b>:c"d|e?f*.xlsx'), 'a_b__c_d_e_f_.xlsx')
  assert.equal(sanitizeFilename('报表\u202Excod.exe'), '报表xcod.exe')
  assert.equal(sanitizeFilename('CON.txt'), '_CON.txt')
  assert.equal(sanitizeFilename('   ...   ', '兜底.xlsx'), '兜底.xlsx')
  assert.equal(sanitizeFilename('', ''), 'export')
})

test('超长文件名按码点截断并保留扩展名', () => {
  const name = '聊'.repeat(300) + '.xlsx'
  const out = sanitizeFilename(name, 'x.xlsx', 120)
  assert.equal(Array.from(out).length, 120)
  assert.ok(out.endsWith('.xlsx'))
})

test('兜底文件名使用北京时间并限定导出格式', () => {
  assert.equal(exportFallbackName('聊天记录', 'xlsx', Date.parse('2026-10-14T11:42:07Z')), '聊天记录-20261014-194207.xlsx')
  assert.equal(normalizeExportFormat('exe'), 'xlsx')
  assert.equal(normalizeExportFormat('txt'), 'txt')
})
