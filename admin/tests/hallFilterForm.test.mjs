import test from 'node:test'
import assert from 'node:assert/strict'

import {
  BUILTIN_GROUPS,
  DEFAULT_FILTER_LIMITS,
  EVENTS_MAX_PAGE_SIZE,
  FILTER_CATEGORY_VALUES,
  MAIL_TO_MAX,
  buildEventsQuery,
  buildFilterPayload,
  buildMailPayload,
  categoryLabel,
  defaultSmtpPort,
  emptyFilterForm,
  emptyMailForm,
  filterSnapshot,
  isLoopbackHost,
  isMailConfigData,
  isValidDomain,
  isValidEmail,
  isValidSmtpHost,
  linesToList,
  listToLines,
  mailSnapshot,
  mailStatusMeta,
  mergeEvents,
  normalizeDomain,
  normalizeDomainLines,
  normalizeEvent,
  normalizeEventList,
  normalizeWordLines,
  oldestEventCursor,
  parseEmailList,
  parseFilterConfig,
  parseFilterLimits,
  parseMailConfig,
  parseTestResult,
  runeLength,
  suggestSmtpPort,
  validateMailForm,
  validateTestContent,
} from '../src/modules/hallFilterForm.js'

const CJK = /[\u4e00-\u9fa5]/

test('多行文本与数组互转：去首尾空白、忽略空行、兼容 CRLF', () => {
  assert.deepEqual(linesToList('  代刷 \r\n\n刷单\r  \n 加群 '), ['代刷', '刷单', '加群'])
  assert.deepEqual(linesToList(''), [])
  assert.deepEqual(linesToList(null), [])
  assert.equal(listToLines(['a', 'b', 3, null, 'c']), 'a\nb\nc')
  assert.equal(listToLines('x'), '')
})

test('按 Unicode 码点计数（与后端 rune 一致）', () => {
  assert.equal(runeLength('百家乐'), 3)
  assert.equal(runeLength('𠮷𠮷'), 2)
  assert.equal(runeLength(5), 0)
})

test('屏蔽词预校验：去重（全半角 / 大小写视为同一词）、单个最多 32 字、条数上限', () => {
  const r = normalizeWordLines(' 代刷\n代刷\nＡＢＣ\nabc\n\n刷单', { maxItems: 10, maxRunes: 32, label: '自定义屏蔽词' })
  assert.deepEqual(r.items, ['代刷', 'ＡＢＣ', '刷单'])
  assert.equal(r.duplicates, 2)
  assert.equal(r.error, '')

  const long = normalizeWordLines('短词\n' + '长'.repeat(33), { maxItems: 10, maxRunes: 32, label: '自定义屏蔽词' })
  assert.deepEqual(long.items, ['短词'])
  assert.equal(long.error, '自定义屏蔽词第 2 行超过 32 个字')
  assert.equal(normalizeWordLines('长'.repeat(32), { maxRunes: 32 }).error, '')

  const many = normalizeWordLines(['a', 'b', 'c'].join('\n'), { maxItems: 2, maxRunes: 32, label: '放行词' })
  assert.equal(many.count, 3)
  assert.equal(many.error, '放行词最多 2 条，当前 3 条')

  assert.match(normalizeWordLines('正常\n坏\u0007词', { label: '放行词' }).error, /第 2 行含有不可见的控制字符/)
  // 与服务端 NormalizeWord 一致：忽略空格 / 标点后相同的词视为重复；只有符号的词拒绝
  const compact = normalizeWordLines('百家乐\n百 家 乐\n百-家-乐', { label: '自定义屏蔽词' })
  assert.deepEqual(compact.items, ['百家乐'])
  assert.equal(compact.duplicates, 2)
  assert.match(normalizeWordLines('***', { label: '自定义屏蔽词' }).error, /自定义屏蔽词第 1 行只有符号或空白/)
})

test('放行域名：整形（小写、去协议 / 路径 / 端口 / 末尾点）、格式校验、去重', () => {
  assert.equal(normalizeDomain(' HTTPS://Shop.Example.COM:8443/path?a=1 '), 'shop.example.com')
  assert.equal(normalizeDomain('example.com.'), 'example.com')
  assert.equal(normalizeDomain('*.Example.com'), 'example.com')
  assert.equal(normalizeDomain('.a.com.'), 'a.com')
  assert.equal(normalizeDomain('ftp://files.example.com#x'), 'files.example.com')
  assert.ok(isValidDomain('example.com'))
  assert.ok(isValidDomain('中文.公司'))
  assert.ok(isValidDomain('a-b.example.co'))
  // 与服务端一致：IP 形式也接受
  assert.ok(isValidDomain('1.2.3.4'))
  for (const bad of ['localhost', '-a.com', 'a..com', 'exa mple.com', '*.example.com', 'a_b.com', '']) {
    assert.equal(isValidDomain(bad), false, bad)
  }
  const r = normalizeDomainLines('Example.com\nhttps://example.com/\nfoo.cn\n\nbad domain', { maxItems: 50 })
  assert.deepEqual(r.items, ['example.com', 'foo.cn'])
  assert.equal(r.duplicates, 1)
  assert.match(r.error, /放行域名第 5 行格式不正确/)
  assert.equal(normalizeDomainLines('a.com\nb.com', { maxItems: 1 }).error, '放行域名最多 1 条，当前 2 条')
})

test('解析 GET /hall/filter：类别只保留 5 个已知值（custom 始终生效不作开关）、limits 优先用接口值', () => {
  const p = parseFilterConfig({
    enabled: true,
    mode: 'whole',
    categories: ['contact', 'custom', 'ad', 'unknown', 'ad'],
    custom_words: ['代刷', 1],
    allow_words: [],
    allow_domains: ['example.com'],
    builtin: { ad: ['加群'], porn: ['x'], gambling: 'bad', contact_keywords: ['微信'] },
    limits: { custom_words: 300, allow_words: '100', allow_domains: -1, word_max_runes: 40 },
  })
  assert.deepEqual(p.form, {
    enabled: true, mode: 'whole', categories: ['ad', 'contact'],
    customWordsText: '代刷', allowWordsText: '', allowDomainsText: 'example.com',
  })
  assert.deepEqual(p.limits, { custom_words: 300, allow_words: 100, allow_domains: 50, word_max_runes: 40 })
  assert.deepEqual(p.builtin, { ad: ['加群'], porn: ['x'], gambling: [], contact_keywords: ['微信'] })
  assert.deepEqual(Object.keys(p.builtin), BUILTIN_GROUPS.map(g => g.key))

  const empty = parseFilterConfig(null)
  assert.deepEqual(empty.form, emptyFilterForm())
  assert.deepEqual(empty.limits, DEFAULT_FILTER_LIMITS)
  assert.equal(parseFilterConfig({ mode: 'evil', enabled: 'true' }).form.mode, 'mask')
  assert.equal(parseFilterConfig({ enabled: 'true' }).form.enabled, false)
  assert.deepEqual(parseFilterLimits({ word_max_runes: '0' }), DEFAULT_FILTER_LIMITS)
})

test('表单 → 保存请求体：六个字段全部存在，词表规范化，非法时给出中文原因', () => {
  const form = {
    enabled: true, mode: 'mask', categories: ['contact', 'ad'],
    customWordsText: ' 代刷 \n代刷\n', allowWordsText: '店铺名', allowDomainsText: 'HTTPS://Example.com/',
  }
  const r = buildFilterPayload(form, { custom_words: 500, allow_words: 200, allow_domains: 50, word_max_runes: 32 })
  assert.deepEqual(r, {
    ok: true,
    body: { enabled: true, mode: 'mask', categories: ['ad', 'contact'], custom_words: ['代刷'], allow_words: ['店铺名'], allow_domains: ['example.com'] },
  })
  assert.deepEqual(buildFilterPayload({ ...form, customWordsText: '', allowWordsText: '', allowDomainsText: '', categories: [] }).body,
    { enabled: true, mode: 'mask', categories: [], custom_words: [], allow_words: [], allow_domains: [] })
  for (const bad of [
    { enabled: 'true' }, { mode: 'all' }, { categories: ['custom'] }, { categories: 'ad' },
    { customWordsText: '长'.repeat(33) }, { allowDomainsText: 'not a domain' },
  ]) {
    const x = buildFilterPayload({ ...form, ...bad })
    assert.equal(x.ok, false, JSON.stringify(bad))
    assert.match(x.error, CJK)
  }
  assert.equal(buildFilterPayload(null).ok, false)
  assert.equal(buildFilterPayload({ ...form, customWordsText: 'a\nb\nc' }, { custom_words: 2 }).error, '自定义屏蔽词最多 2 条，当前 3 条')
  assert.deepEqual(FILTER_CATEGORY_VALUES, ['ad', 'link', 'porn', 'gambling', 'contact'])
})

test('屏蔽规则脏数据检测：空白、空行、勾选顺序不算修改；内容变化算修改', () => {
  const base = { enabled: true, mode: 'mask', categories: ['ad', 'contact'], customWordsText: '代刷\n刷单', allowWordsText: '', allowDomainsText: 'example.com' }
  const same = { ...base, categories: ['contact', 'ad'], customWordsText: ' 代刷 \n\n刷单\n', allowDomainsText: 'Example.COM' }
  assert.equal(filterSnapshot(same), filterSnapshot(base))
  assert.notEqual(filterSnapshot({ ...base, enabled: false }), filterSnapshot(base))
  assert.notEqual(filterSnapshot({ ...base, mode: 'whole' }), filterSnapshot(base))
  assert.notEqual(filterSnapshot({ ...base, customWordsText: '代刷' }), filterSnapshot(base))
  assert.notEqual(filterSnapshot({ ...base, categories: ['ad'] }), filterSnapshot(base))
})

test('检测试用：内容 1–1000 字（码点）、结果解析', () => {
  assert.equal(validateTestContent('  ').ok, false)
  assert.equal(validateTestContent(null).ok, false)
  assert.equal(validateTestContent('字'.repeat(1000)).ok, true)
  assert.match(validateTestContent('字'.repeat(1001)).error, /最多 1000 个字/)
  assert.deepEqual(parseTestResult({ filtered: true, masked: '加我**', categories: ['contact', 'contact'], hits: [{ category: 'contact', text: '微信abc123456' }, null, { text: 5 }] }),
    { filtered: true, masked: '加我**', categories: ['contact'], hits: [{ category: 'contact', text: '微信abc123456' }] })
  assert.deepEqual(parseTestResult(undefined), { filtered: false, masked: '', categories: [], hits: [] })
  assert.equal(categoryLabel('custom'), '自定义词')
  assert.equal(categoryLabel('gambling'), '博彩')
  assert.equal(categoryLabel('__proto__'), '其他')
})

test('邮箱与 SMTP 主机校验', () => {
  for (const ok of ['qa@example.test', 'kelili-test@example.test', 'a.b+c@sub.example.co', 'x_y@163.com']) assert.ok(isValidEmail(ok), ok)
  for (const bad of ['', 'a@b', '@x.com', 'a@', 'a b@x.com', 'a@x .com', '.a@x.com', 'a..b@x.com', 'a@x.com\r\nBcc: z@y.com', 'a@-x.com', 'a<b>@x.com', '"a"@x.com', 'a,b@x.com', 'a'.repeat(65) + '@x.com', 'Name <a@x.com>']) {
    assert.equal(isValidEmail(bad), false, bad)
  }
  for (const ok of ['smtp.163.com', 'localhost', 'LOCALHOST', '127.0.0.1', '::1', '[::1]', 'smtp.qq.com']) assert.ok(isValidSmtpHost(ok), ok)
  // 与服务端一致：单段主机名只接受 localhost
  for (const bad of ['smtp://x.com', 'x.com:465', 'a b', '999.1.1.1', '', 'x_y.com', 'mail-relay', 'x.com/path']) assert.equal(isValidSmtpHost(bad), false, bad)
  for (const ok of ['localhost', '127.0.0.1', '127.1.2.3', '::1', '[::1]', ' LOCALHOST ']) assert.ok(isLoopbackHost(ok), ok)
  for (const bad of ['smtp.163.com', '10.0.0.1', '128.0.0.1', '0.0.0.0', 'localhost.evil.com', null]) assert.equal(isLoopbackHost(bad), false, String(bad))
})

test('收件邮箱：一行一个（也接受逗号 / 分号）、不区分大小写去重、1–5 个', () => {
  const r = parseEmailList('qa@example.test\nQA@example.test, b@example.test；c@example.test')
  assert.deepEqual(r.items, ['qa@example.test', 'b@example.test', 'c@example.test'])
  assert.equal(r.duplicates, 1)
  assert.equal(r.error, '')
  assert.equal(parseEmailList('').error, '开启邮件通知时至少填写 1 个收件邮箱')
  assert.equal(parseEmailList('', { required: false }).error, '')
  assert.equal(parseEmailList(MAIL_TO_MAX + '', { required: false }).error, '收件邮箱格式不正确：5')
  assert.match(parseEmailList('ok@example.test\nnot-mail').error, /收件邮箱格式不正确：not-mail/)
  const six = Array.from({ length: MAIL_TO_MAX + 1 }, (_, i) => 'u' + i + '@example.test').join('\n')
  assert.equal(parseEmailList(six).error, '收件邮箱最多 5 个，当前 6 个')
})

test('解析 GET /hall/alert-mail：不回显密码、端口与加密方式兜底，前端不写死邮箱', () => {
  const p = parseMailConfig({
    enabled: true, to: ['a@example.test', 'b@example.test'], from: 'f@example.test', smtp_host: 'smtp.example.test',
    smtp_port: 587, smtp_security: 'starttls', smtp_username: 'u', password_set: true, configured: true, smtp_password: 'leak',
  })
  assert.deepEqual(p, {
    form: { enabled: true, toText: 'a@example.test\nb@example.test', from: 'f@example.test', smtp_host: 'smtp.example.test', smtp_port: 587, smtp_security: 'starttls', smtp_username: 'u', smtp_password: '' },
    passwordSet: true,
    configured: true,
  })
  const d = parseMailConfig({ smtp_security: 'tls', smtp_port: 70000 })
  assert.equal(d.form.smtp_security, 'ssl')
  assert.equal(d.form.smtp_port, 465)
  assert.equal(parseMailConfig({ smtp_port: '2525', smtp_security: 'none' }).form.smtp_port, 2525)
  const empty = parseMailConfig(null)
  assert.deepEqual(empty.form, emptyMailForm())
  assert.equal(empty.form.toText, '')
  assert.equal(empty.form.from, '')
  assert.equal(empty.passwordSet, false)
  assert.equal(empty.configured, false)
})

test('邮件表单 → 请求体：密码留空不传；不加密只允许本机；开启时必填项', () => {
  const form = {
    enabled: true, toText: 'qa@example.test', from: ' kelili-test@example.test ', smtp_host: ' 127.0.0.1 ',
    smtp_port: 2525, smtp_security: 'none', smtp_username: ' qa ', smtp_password: '',
  }
  const r = buildMailPayload(form)
  assert.deepEqual(r, {
    ok: true,
    body: { enabled: true, to: ['qa@example.test'], from: 'kelili-test@example.test', smtp_host: '127.0.0.1', smtp_port: 2525, smtp_security: 'none', smtp_username: 'qa' },
  })
  assert.equal(Object.prototype.hasOwnProperty.call(r.body, 'smtp_password'), false)
  assert.equal(buildMailPayload({ ...form, smtp_password: 'qa-pass' }).body.smtp_password, 'qa-pass')
  // 密码不做 trim，原样提交
  assert.equal(buildMailPayload({ ...form, smtp_password: ' p w ' }).body.smtp_password, ' p w ')

  const cases = [
    [{ smtp_host: 'smtp.163.com' }, /不加密.*只允许本机地址/],
    [{ toText: '' }, /至少填写 1 个收件邮箱/],
    [{ from: '' }, /请填写发件邮箱/],
    [{ from: 'bad' }, /发件邮箱格式不正确/],
    [{ smtp_host: '' }, /请填写 SMTP 服务器/],
    [{ smtp_host: 'smtp://x.com' }, /SMTP 服务器格式不正确/],
    [{ smtp_port: 0 }, /端口必须是 1–65535/],
    [{ smtp_port: 65536 }, /端口必须是 1–65535/],
    [{ smtp_port: null }, /端口必须是 1–65535/],
    [{ smtp_port: 25.5 }, /端口必须是 1–65535/],
    [{ smtp_security: 'tls' }, /请选择加密方式/],
    [{ smtp_password: '   ' }, /不能只包含空格/],
    [{ smtp_password: 'a\r\nb' }, /控制字符/],
    [{ smtp_username: 'a\nb' }, /控制字符/],
    [{ enabled: 'true' }, /开关取值无效/],
    [{ smtp_username: '用'.repeat(129) }, /用户名最多 128 个字符/],
    [{ smtp_password: 'p'.repeat(257) }, /密码最多 256 个字符/],
  ]
  for (const [patch, re] of cases) {
    const x = buildMailPayload({ ...form, ...patch })
    assert.equal(x.ok, false, JSON.stringify(patch))
    assert.match(x.error, re, JSON.stringify(patch))
  }
  // 与服务端一致：关闭时收件邮箱可以为空；发件邮箱与 SMTP 服务器始终必填；收件邮箱最多 5 个始终校验
  const off = buildMailPayload({ ...form, enabled: false, toText: '' })
  assert.equal(off.ok, true)
  assert.deepEqual(off.body.to, [])
  assert.equal(buildMailPayload({ ...form, enabled: false, from: '' }).ok, false)
  assert.equal(buildMailPayload({ ...form, enabled: false, smtp_host: '' }).ok, false)
  assert.equal(buildMailPayload({ ...form, enabled: false, toText: 'a@x.co,b@x.co,c@x.co,d@x.co,e@x.co,f@x.co' }).ok, false)
  assert.equal(buildMailPayload({ ...form, smtp_security: 'ssl', smtp_host: 'smtp.163.com', smtp_port: 465 }).ok, true)
  assert.equal(validateMailForm({ ...form, smtp_security: 'starttls', smtp_host: 'smtp.163.com' }).errors.smtp_security, '')
})

test('切换加密方式时的端口建议：默认端口跟随，自填端口保留', () => {
  assert.equal(defaultSmtpPort('ssl'), 465)
  assert.equal(defaultSmtpPort('starttls'), 587)
  assert.equal(defaultSmtpPort('none'), 25)
  assert.equal(suggestSmtpPort('ssl', 'starttls', 465), 587)
  assert.equal(suggestSmtpPort('starttls', 'none', 587), 25)
  assert.equal(suggestSmtpPort('ssl', 'none', 2525), 2525)
  assert.equal(suggestSmtpPort('ssl', 'starttls', null), 587)
  assert.equal(suggestSmtpPort('unknown', 'ssl', 25), 25)
})

test('邮件设置脏数据检测：密码不进快照，只看是否填写了新密码', () => {
  const base = { enabled: true, toText: 'a@example.test', from: 'f@example.test', smtp_host: 'h.example.test', smtp_port: 465, smtp_security: 'ssl', smtp_username: 'u', smtp_password: '' }
  assert.equal(mailSnapshot({ ...base, toText: ' A@example.test \n', from: ' f@example.test' }), mailSnapshot(base))
  assert.notEqual(mailSnapshot({ ...base, smtp_password: 'x' }), mailSnapshot(base))
  assert.ok(!mailSnapshot({ ...base, smtp_password: 'secret-value' }).includes('secret-value'))
  assert.notEqual(mailSnapshot({ ...base, smtp_port: 587 }), mailSnapshot(base))
  assert.notEqual(mailSnapshot({ ...base, enabled: false }), mailSnapshot(base))
  assert.ok(isMailConfigData({ to: [], password_set: false, configured: false }))
  assert.equal(isMailConfigData({ to: [] }), false)
  assert.equal(isMailConfigData(null), false)
  assert.equal(isMailConfigData([]), false)
})

test('邮件状态文案映射', () => {
  assert.deepEqual(mailStatusMeta('pending'), { label: '待发送', type: 'warning' })
  assert.deepEqual(mailStatusMeta('sent'), { label: '已发送', type: 'success' })
  assert.deepEqual(mailStatusMeta('failed'), { label: '发送失败', type: 'danger' })
  assert.deepEqual(mailStatusMeta('disabled'), { label: '未开启', type: 'info' })
  assert.deepEqual(mailStatusMeta('silenced'), { label: '已拉黑不发送', type: 'info' })
  assert.equal(mailStatusMeta('x').label, '未知')
  assert.equal(mailStatusMeta('toString').label, '未知')
})

test('拦截记录：游标与 limit 校验、记录规范化、翻页合并去重', () => {
  assert.deepEqual(buildEventsQuery(), { ok: true, params: { limit: 20 } })
  assert.deepEqual(buildEventsQuery({ before: 123, limit: 999 }), { ok: true, params: { limit: EVENTS_MAX_PAGE_SIZE, before: '123' } })
  assert.deepEqual(buildEventsQuery({ before: '42', limit: 0 }), { ok: true, params: { limit: 1, before: '42' } })
  for (const bad of ['../x', 'a b', 'abc', '0', '01', '1'.repeat(20), -1, 0, 1.5]) assert.equal(buildEventsQuery({ before: bad }).ok, false, String(bad))

  const raw = { id: 7, created_at: '2026-10-18T02:00:00.000+08:00', sender: 'visitor', name: '访客 1234', identity: 'v-1', vid: 'v_abc', categories: ['contact', 'porn', 'contact'], original: '加我微信 abc123456 看片', masked: '加我** **', mail_status: 'sent', mail_attempts: 1, mail_error: '' }
  const e = normalizeEvent(raw)
  assert.equal(e.key, '7')
  assert.deepEqual(e.categories, ['contact', 'porn'])
  assert.equal(e.mailStatus, 'sent')
  assert.equal(e.mailAttempts, 1)
  assert.equal(normalizeEvent({ id: '../x' }), null)
  assert.equal(normalizeEvent(null), null)
  assert.equal(normalizeEvent({ id: 1, original: 'x'.repeat(3000) }).original.length, 2001)

  const page1 = normalizeEventList([raw, { ...raw, id: 6 }, { id: '' }])
  assert.equal(page1.length, 2)
  const merged = mergeEvents(page1, normalizeEventList([{ ...raw, id: 6 }, { ...raw, id: 5 }]))
  assert.deepEqual(merged.map(x => x.id), [7, 6, 5])
  assert.equal(oldestEventCursor(merged), 5)
  assert.equal(oldestEventCursor([]), '')
  assert.deepEqual(normalizeEventList('x'), [])
})
