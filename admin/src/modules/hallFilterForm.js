// 多人大厅「敏感词屏蔽 / 邮件告警」设置页的纯逻辑（契约 _internal/reports/hall-contract.md 第 9 节）。
// 只做前端预校验与数据整形（可在 node 测试中直接运行），最终以服务端校验为准。
// 本模块不含任何默认邮箱：收件/发件默认值由服务器环境变量提供，前端只展示接口返回值。

// ---------- 屏蔽规则 ----------

export const FILTER_CATEGORIES = Object.freeze([
  Object.freeze({ value: 'ad', label: '广告' }),
  Object.freeze({ value: 'link', label: '超链接' }),
  Object.freeze({ value: 'porn', label: '色情' }),
  Object.freeze({ value: 'gambling', label: '博彩' }),
  Object.freeze({ value: 'contact', label: '联系方式' }),
])

export const FILTER_CATEGORY_VALUES = Object.freeze(FILTER_CATEGORIES.map(c => c.value))

const CATEGORY_LABELS = Object.freeze({
  ad: '广告',
  link: '超链接',
  porn: '色情',
  gambling: '博彩',
  contact: '联系方式',
  custom: '自定义词',
})

export const FILTER_MODES = Object.freeze([
  Object.freeze({ value: 'mask', label: '只把敏感部分替换为 **' }),
  Object.freeze({ value: 'whole', label: '整条消息替换为 **' }),
])

const FILTER_MODE_VALUES = FILTER_MODES.map(m => m.value)
export const DEFAULT_FILTER_MODE = 'mask'

// 内置词库只读展示的分组（GET /api/admin/hall/filter 的 data.builtin）
export const BUILTIN_GROUPS = Object.freeze([
  Object.freeze({ key: 'ad', label: '广告' }),
  Object.freeze({ key: 'porn', label: '色情' }),
  Object.freeze({ key: 'gambling', label: '博彩' }),
  Object.freeze({ key: 'contact_keywords', label: '联系方式关键词' }),
])

// 接口未返回 limits 时的兜底（与契约第 9.2 节一致）；接口返回值优先
export const DEFAULT_FILTER_LIMITS = Object.freeze({
  custom_words: 500,
  allow_words: 200,
  allow_domains: 50,
  word_max_runes: 32,
})

const LIMIT_CEILING = 100000
// 域名按 DNS 上限 253 字符校验（服务端会把中文域名转 punycode 再判断，前端放宽，以服务端为准）
export const DOMAIN_MAX_CHARS = 253

const hasOwn = (obj, key) => Object.prototype.hasOwnProperty.call(obj, key)
// C0 控制字符与 DEL：词表、账号等字段中出现基本都是误粘贴，且可能被用于邮件头注入
const CONTROL_CHARS = /[\u0000-\u001f\u007f]/

export function categoryLabel(value) {
  return typeof value === 'string' && hasOwn(CATEGORY_LABELS, value) ? CATEGORY_LABELS[value] : '其他'
}

// 按 Unicode 码点计数（与后端 Go 的 rune 口径一致）
export function runeLength(value) {
  return typeof value === 'string' ? Array.from(value).length : 0
}

export function splitLines(text) {
  if (typeof text !== 'string' || text === '') return []
  return text.split(/\r\n|\r|\n/)
}

// 多行文本 → 去首尾空白、去空行后的数组（不去重，用于计数与脏数据检测）
export function linesToList(text) {
  return splitLines(text).map(s => s.trim()).filter(Boolean)
}

// 数组 → 多行文本（只接受字符串项）
export function listToLines(list) {
  return stringList(list).join('\n')
}

function stringList(value) {
  return Array.isArray(value) ? value.filter(v => typeof v === 'string') : []
}

function positiveIntOr(value, def) {
  const n = typeof value === 'string' && value.trim() !== '' ? Number(value.trim()) : value
  return typeof n === 'number' && Number.isInteger(n) && n > 0 && n <= LIMIT_CEILING ? n : def
}

export function parseFilterLimits(raw) {
  const d = raw && typeof raw === 'object' ? raw : {}
  const out = {}
  for (const [key, def] of Object.entries(DEFAULT_FILTER_LIMITS)) out[key] = positiveIntOr(d[key], def)
  return out
}

// 去重键：与服务端 textfilter.NormalizeWord 口径一致——全角转半角、小写、只保留文字与数字
// （服务端匹配忽略空格 / 标点 / 零宽字符，所以「百 家 乐」与「百家乐」是同一个词）
function compactWord(value) {
  return value.normalize('NFKC').toLowerCase().replace(/[^\p{L}\p{Nd}]/gu, '')
}

/**
 * 屏蔽词 / 放行词预校验：去首尾空白、忽略空行、去重、单个最多 maxRunes 个字、条数上限 maxItems。
 * 返回 { items, count, duplicates, errors, error }，error 为第一条错误（无错误为空串）。
 */
export function normalizeWordLines(text, { maxItems = DEFAULT_FILTER_LIMITS.custom_words, maxRunes = DEFAULT_FILTER_LIMITS.word_max_runes, label = '词表' } = {}) {
  const items = []
  const errors = []
  const seen = new Set()
  let duplicates = 0
  splitLines(text).forEach((raw, idx) => {
    const v = raw.trim()
    if (!v) return
    if (CONTROL_CHARS.test(v)) {
      errors.push(label + '第 ' + (idx + 1) + ' 行含有不可见的控制字符')
      return
    }
    if (runeLength(v) > maxRunes) {
      errors.push(label + '第 ' + (idx + 1) + ' 行超过 ' + maxRunes + ' 个字')
      return
    }
    const key = compactWord(v)
    if (!key) {
      errors.push(label + '第 ' + (idx + 1) + ' 行只有符号或空白（至少要有一个文字或数字）')
      return
    }
    if (seen.has(key)) {
      duplicates++
      return
    }
    seen.add(key)
    items.push(v)
  })
  if (items.length > maxItems) errors.push(label + '最多 ' + maxItems + ' 条，当前 ' + items.length + ' 条')
  return { items, count: items.length, duplicates, errors, error: errors[0] || '' }
}

// 单个域名整形（与服务端 normalizeDomain 一致）：转小写，去掉误粘贴的协议头、路径、查询、端口、前导「*.」和首尾的点
export function normalizeDomain(raw) {
  if (typeof raw !== 'string') return ''
  let v = raw.trim().toLowerCase()
  const scheme = v.indexOf('://')
  if (scheme >= 0) v = v.slice(scheme + 3)
  v = v.split(/[/?#]/, 1)[0]
  v = v.replace(/:\d{1,5}$/, '')
  if (v.startsWith('*.')) v = v.slice(2)
  v = v.replace(/^\.+|\.+$/g, '')
  return v
}

const DOMAIN_LABEL = '[\\p{L}\\p{N}](?:[\\p{L}\\p{N}-]{0,61}[\\p{L}\\p{N}])?'
const DOMAIN_RE = new RegExp('^(?:' + DOMAIN_LABEL + '\\.)+' + DOMAIN_LABEL + '$', 'u')

// 至少两段、每段字母数字开头结尾（中文域名由服务端转 punycode 后再严格校验）
export function isValidDomain(value) {
  if (typeof value !== 'string' || !value || value.length > DOMAIN_MAX_CHARS) return false
  return DOMAIN_RE.test(value)
}

/**
 * 放行域名预校验：整形 + 格式校验 + 去重 + 条数上限。返回结构同 normalizeWordLines。
 */
export function normalizeDomainLines(text, { maxItems = DEFAULT_FILTER_LIMITS.allow_domains, label = '放行域名' } = {}) {
  const items = []
  const errors = []
  const seen = new Set()
  let duplicates = 0
  splitLines(text).forEach((raw, idx) => {
    if (!raw.trim()) return
    if (CONTROL_CHARS.test(raw.trim())) {
      errors.push(label + '第 ' + (idx + 1) + ' 行含有不可见的控制字符')
      return
    }
    const v = normalizeDomain(raw)
    if (!isValidDomain(v)) {
      errors.push(label + '第 ' + (idx + 1) + ' 行格式不正确（只填域名，例如 example.com）')
      return
    }
    if (seen.has(v)) {
      duplicates++
      return
    }
    seen.add(v)
    items.push(v)
  })
  if (items.length > maxItems) errors.push(label + '最多 ' + maxItems + ' 条，当前 ' + items.length + ' 条')
  return { items, count: items.length, duplicates, errors, error: errors[0] || '' }
}

export function emptyFilterForm() {
  return {
    enabled: false,
    mode: DEFAULT_FILTER_MODE,
    categories: [],
    customWordsText: '',
    allowWordsText: '',
    allowDomainsText: '',
  }
}

function knownCategories(value) {
  const set = new Set(stringList(value))
  // 按固定顺序输出，忽略 custom（自定义词始终生效，不是开关）与未知值
  return FILTER_CATEGORY_VALUES.filter(v => set.has(v))
}

// GET /api/admin/hall/filter 的 data → { form, limits, builtin }
export function parseFilterConfig(data) {
  const d = data && typeof data === 'object' ? data : {}
  const b = d.builtin && typeof d.builtin === 'object' ? d.builtin : {}
  const builtin = {}
  for (const g of BUILTIN_GROUPS) builtin[g.key] = stringList(b[g.key])
  return {
    form: {
      enabled: d.enabled === true,
      mode: FILTER_MODE_VALUES.includes(d.mode) ? d.mode : DEFAULT_FILTER_MODE,
      categories: knownCategories(d.categories),
      customWordsText: listToLines(d.custom_words),
      allowWordsText: listToLines(d.allow_words),
      allowDomainsText: listToLines(d.allow_domains),
    },
    limits: parseFilterLimits(d.limits),
    builtin,
  }
}

// 三个词表的预校验结果（组件计数显示与保存共用）
export function checkFilterLists(form, limits = DEFAULT_FILTER_LIMITS) {
  const f = form && typeof form === 'object' ? form : {}
  const lim = parseFilterLimits(limits)
  return {
    custom: normalizeWordLines(f.customWordsText, { maxItems: lim.custom_words, maxRunes: lim.word_max_runes, label: '自定义屏蔽词' }),
    allow: normalizeWordLines(f.allowWordsText, { maxItems: lim.allow_words, maxRunes: lim.word_max_runes, label: '放行词' }),
    domains: normalizeDomainLines(f.allowDomainsText, { maxItems: lim.allow_domains, label: '放行域名' }),
  }
}

/**
 * 表单 → POST /api/admin/hall/filter 请求体（也用作 /filter/test 的 config）。
 * 返回 { ok: true, body } 或 { ok: false, error }。
 */
export function buildFilterPayload(form, limits = DEFAULT_FILTER_LIMITS) {
  const f = form && typeof form === 'object' ? form : null
  if (!f || typeof f.enabled !== 'boolean') return { ok: false, error: '启用开关取值无效，请刷新页面后重试' }
  if (!FILTER_MODE_VALUES.includes(f.mode)) return { ok: false, error: '请选择处理方式' }
  if (!Array.isArray(f.categories) || f.categories.some(c => !FILTER_CATEGORY_VALUES.includes(c))) {
    return { ok: false, error: '拦截类别取值无效，请刷新页面后重试' }
  }
  const lists = checkFilterLists(f, limits)
  for (const r of [lists.custom, lists.allow, lists.domains]) {
    if (r.error) return { ok: false, error: r.error }
  }
  return {
    ok: true,
    body: {
      enabled: f.enabled,
      mode: f.mode,
      categories: knownCategories(f.categories),
      custom_words: lists.custom.items,
      allow_words: lists.allow.items,
      allow_domains: lists.domains.items,
    },
  }
}

// 脏数据检测快照：只比较有意义的差异（首尾空白、空行、类别勾选顺序不算修改）
export function filterSnapshot(form) {
  const f = form && typeof form === 'object' ? form : {}
  return JSON.stringify({
    enabled: f.enabled === true,
    mode: typeof f.mode === 'string' ? f.mode : '',
    categories: knownCategories(f.categories),
    custom: linesToList(f.customWordsText),
    allow: linesToList(f.allowWordsText),
    domains: linesToList(f.allowDomainsText).map(normalizeDomain),
  })
}

// ---------- 检测试用 ----------

export const TEST_CONTENT_MAX = 1000

export function validateTestContent(text) {
  if (typeof text !== 'string' || !text.trim()) return { ok: false, error: '请输入要检测的文字' }
  const n = runeLength(text)
  if (n > TEST_CONTENT_MAX) return { ok: false, error: '检测文字最多 ' + TEST_CONTENT_MAX + ' 个字，当前 ' + n + ' 个字' }
  return { ok: true }
}

// POST /api/admin/hall/filter/test 的 data → 统一结构
export function parseTestResult(data) {
  const d = data && typeof data === 'object' ? data : {}
  const hits = Array.isArray(d.hits)
    ? d.hits
      .filter(h => h && typeof h === 'object' && typeof h.text === 'string')
      .map(h => ({ category: typeof h.category === 'string' ? h.category : '', text: h.text }))
    : []
  return {
    filtered: d.filtered === true,
    masked: typeof d.masked === 'string' ? d.masked : '',
    categories: Array.from(new Set(stringList(d.categories))),
    hits,
  }
}

// ---------- 邮件通知 ----------

export const SMTP_SECURITY_OPTIONS = Object.freeze([
  Object.freeze({ value: 'ssl', label: 'SSL', defaultPort: 465 }),
  Object.freeze({ value: 'starttls', label: 'STARTTLS', defaultPort: 587 }),
  Object.freeze({ value: 'none', label: '不加密（仅本机）', defaultPort: 25 }),
])

const SECURITY_VALUES = SMTP_SECURITY_OPTIONS.map(o => o.value)
export const DEFAULT_SMTP_SECURITY = 'ssl'
export const MAIL_TO_MAX = 5
export const EMAIL_MAX_CHARS = 254
export const SMTP_HOST_MAX_CHARS = 253
// 与服务端 validateAlertInput 一致：用户名最多 128 字、密码最多 256 字（按码点）
export const SMTP_USERNAME_MAX_CHARS = 128
export const SMTP_PASSWORD_MAX_CHARS = 256
export const SMTP_PORT_MIN = 1
export const SMTP_PORT_MAX = 65535

export function defaultSmtpPort(security) {
  const opt = SMTP_SECURITY_OPTIONS.find(o => o.value === security)
  return opt ? opt.defaultPort : SMTP_SECURITY_OPTIONS[0].defaultPort
}

export function isValidSmtpPort(port) {
  return typeof port === 'number' && Number.isInteger(port) && port >= SMTP_PORT_MIN && port <= SMTP_PORT_MAX
}

// 切换加密方式时：端口还是旧方式的默认端口（或无效）才跟着换成新方式的默认端口，用户自填的端口保持不变
export function suggestSmtpPort(prevSecurity, nextSecurity, currentPort) {
  if (!isValidSmtpPort(currentPort)) return defaultSmtpPort(nextSecurity)
  const known = SMTP_SECURITY_OPTIONS.some(o => o.value === prevSecurity)
  if (known && currentPort === defaultSmtpPort(prevSecurity)) return defaultSmtpPort(nextSecurity)
  return currentPort
}

const HOST_LABEL_RE = /^[\p{L}\p{N}](?:[\p{L}\p{N}-]{0,61}[\p{L}\p{N}])?$/u
const EMAIL_LOCAL_RE = /^[\p{L}\p{N}!#$%&'*+/=?^_\x60{|}~.-]+$/u
const IPV4_RE = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/

// 主机名：至少两段（与服务端一致，单段名只接受 localhost，由调用方处理）
function isValidHostname(value) {
  if (typeof value !== 'string' || !value || value.length > SMTP_HOST_MAX_CHARS) return false
  const labels = value.split('.')
  return labels.length >= 2 && labels.every(l => HOST_LABEL_RE.test(l))
}

function isValidIPv6(value) {
  const v = value.startsWith('[') && value.endsWith(']') ? value.slice(1, -1) : value
  if (!v.includes(':') || !/^[0-9A-Fa-f:.]+$/.test(v)) return false
  if ((v.match(/::/g) || []).length > 1) return false
  const groups = v.split(':')
  return groups.length >= 3 && groups.length <= 8 && groups.every(g => g.length <= 4 || IPV4_RE.test(g))
}

export function isValidEmail(value) {
  if (typeof value !== 'string' || !value || value.length > EMAIL_MAX_CHARS) return false
  const at = value.lastIndexOf('@')
  if (at < 1 || at === value.length - 1) return false
  const local = value.slice(0, at)
  const domain = value.slice(at + 1)
  if (local.length > 64 || !EMAIL_LOCAL_RE.test(local)) return false
  if (local.startsWith('.') || local.endsWith('.') || local.includes('..')) return false
  return isValidHostname(domain)
}

// SMTP 服务器：IP、localhost 或至少两段的域名（不带协议、端口、路径）
export function isValidSmtpHost(value) {
  if (typeof value !== 'string' || !value) return false
  if (value.toLowerCase() === 'localhost') return true
  if (IPV4_RE.test(value)) return true
  // 纯数字点分却不是合法 IPv4（如 999.1.1.1）一定是输错了
  if (/^[\d.]+$/.test(value)) return false
  if (value.includes(':')) return isValidIPv6(value)
  return isValidHostname(value)
}

// 「不加密」只允许本机地址（契约第 9.2 节：防止明文传密码）
export function isLoopbackHost(value) {
  if (typeof value !== 'string') return false
  const v = value.trim().toLowerCase()
  if (v === 'localhost' || v === '::1' || v === '[::1]' || v === '0:0:0:0:0:0:0:1') return true
  return IPV4_RE.test(v) && v.startsWith('127.')
}

/**
 * 收件邮箱：一行一个（也接受逗号、分号、空格分隔），去重（不区分大小写），最多 5 个且格式正确；
 * required 为 true（开启邮件通知）时至少 1 个（与服务端一致：关闭时允许为空）。
 * 返回 { items, count, duplicates, errors, error }。
 */
export function parseEmailList(text, { required = true } = {}) {
  const items = []
  const errors = []
  const seen = new Set()
  let duplicates = 0
  const parts = typeof text === 'string' ? text.split(/[\s,;，；]+/).filter(Boolean) : []
  for (const p of parts) {
    if (!isValidEmail(p)) {
      errors.push('收件邮箱格式不正确：' + (p.length > 60 ? p.slice(0, 60) + '…' : p))
      continue
    }
    const key = p.toLowerCase()
    if (seen.has(key)) {
      duplicates++
      continue
    }
    seen.add(key)
    items.push(p)
  }
  if (required && !errors.length && items.length === 0) errors.push('开启邮件通知时至少填写 1 个收件邮箱')
  if (items.length > MAIL_TO_MAX) errors.push('收件邮箱最多 ' + MAIL_TO_MAX + ' 个，当前 ' + items.length + ' 个')
  return { items, count: items.length, duplicates, errors, error: errors[0] || '' }
}

export function emptyMailForm() {
  return {
    enabled: false,
    toText: '',
    from: '',
    smtp_host: '',
    smtp_port: defaultSmtpPort(DEFAULT_SMTP_SECURITY),
    smtp_security: DEFAULT_SMTP_SECURITY,
    smtp_username: '',
    smtp_password: '',
  }
}

const str = v => (typeof v === 'string' ? v : '')

// GET /api/admin/hall/alert-mail 的 data → { form, passwordSet, configured }（接口永不返回密码，表单密码恒为空）
export function parseMailConfig(data) {
  const d = data && typeof data === 'object' ? data : {}
  const security = SECURITY_VALUES.includes(d.smtp_security) ? d.smtp_security : DEFAULT_SMTP_SECURITY
  const port = typeof d.smtp_port === 'string' && d.smtp_port.trim() !== '' ? Number(d.smtp_port.trim()) : d.smtp_port
  return {
    form: {
      enabled: d.enabled === true,
      toText: listToLines(d.to),
      from: str(d.from),
      smtp_host: str(d.smtp_host),
      smtp_port: isValidSmtpPort(port) ? port : defaultSmtpPort(security),
      smtp_security: security,
      smtp_username: str(d.smtp_username),
      smtp_password: '',
    },
    passwordSet: d.password_set === true,
    configured: d.configured === true,
  }
}

/**
 * 邮件设置逐项校验，返回 { errors: {to, from, smtp_host, smtp_port, smtp_security, smtp_username, smtp_password}, to }。
 * 每项为空串表示通过；用于表单项错误提示与保存前校验。
 */
export function validateMailForm(form) {
  const f = form && typeof form === 'object' ? form : {}
  const enabled = f.enabled === true
  const errors = { enabled: '', to: '', from: '', smtp_host: '', smtp_port: '', smtp_security: '', smtp_username: '', smtp_password: '' }
  if (typeof f.enabled !== 'boolean') errors.enabled = '启用开关取值无效，请刷新页面后重试'
  const to = parseEmailList(f.toText, { required: enabled })
  errors.to = to.error
  // 发件邮箱与 SMTP 服务器无论是否开启都必填（服务端始终校验）
  const from = str(f.from).trim()
  if (!from) {
    errors.from = '请填写发件邮箱'
  } else if (!isValidEmail(from)) {
    errors.from = '发件邮箱格式不正确'
  }
  const host = str(f.smtp_host).trim()
  if (!host) {
    errors.smtp_host = '请填写 SMTP 服务器'
  } else if (!isValidSmtpHost(host)) {
    errors.smtp_host = 'SMTP 服务器格式不正确（填写域名或 IP，不要带 smtp:// 或端口）'
  }
  if (!isValidSmtpPort(f.smtp_port)) errors.smtp_port = '端口必须是 ' + SMTP_PORT_MIN + '–' + SMTP_PORT_MAX + ' 之间的整数'
  if (!SECURITY_VALUES.includes(f.smtp_security)) {
    errors.smtp_security = '请选择加密方式'
  } else if (f.smtp_security === 'none' && host && !isLoopbackHost(host)) {
    errors.smtp_security = '「不加密」只允许本机地址（localhost、127.0.0.1、::1），否则密码会明文传输'
  }
  const username = str(f.smtp_username).trim()
  if (runeLength(username) > SMTP_USERNAME_MAX_CHARS) errors.smtp_username = '用户名最多 ' + SMTP_USERNAME_MAX_CHARS + ' 个字符'
  else if (CONTROL_CHARS.test(username)) errors.smtp_username = '用户名含有不可见的控制字符'
  const password = str(f.smtp_password)
  if (password) {
    if (!password.trim()) errors.smtp_password = '密码不能只包含空格'
    else if (runeLength(password) > SMTP_PASSWORD_MAX_CHARS) errors.smtp_password = '密码最多 ' + SMTP_PASSWORD_MAX_CHARS + ' 个字符'
    else if (CONTROL_CHARS.test(password)) errors.smtp_password = '密码含有不可见的控制字符'
  }
  return { errors, to: to.items }
}

const MAIL_ERROR_ORDER = ['enabled', 'to', 'from', 'smtp_host', 'smtp_security', 'smtp_port', 'smtp_username', 'smtp_password']

/**
 * 表单 → POST /api/admin/hall/alert-mail 请求体；密码为空时不传 smtp_password（= 不修改）。
 * 返回 { ok: true, body } 或 { ok: false, error }。
 */
export function buildMailPayload(form) {
  const f = form && typeof form === 'object' ? form : {}
  const { errors, to } = validateMailForm(f)
  for (const key of MAIL_ERROR_ORDER) {
    if (errors[key]) return { ok: false, error: errors[key] }
  }
  const body = {
    enabled: f.enabled,
    to,
    from: str(f.from).trim(),
    smtp_host: str(f.smtp_host).trim(),
    smtp_port: f.smtp_port,
    smtp_security: f.smtp_security,
    smtp_username: str(f.smtp_username).trim(),
  }
  const password = str(f.smtp_password)
  if (password) body.smtp_password = password
  return { ok: true, body }
}

// 判断接口返回是否为完整的邮件配置对象（POST 保存接口未约定返回体时，用它决定直接套用还是重新 GET）
export function isMailConfigData(data) {
  return !!data && typeof data === 'object' && !Array.isArray(data) &&
    typeof data.password_set === 'boolean' && typeof data.configured === 'boolean' && Array.isArray(data.to)
}

// 邮件设置脏数据快照：密码不进快照，只记录「是否填写了新密码」
export function mailSnapshot(form) {
  const f = form && typeof form === 'object' ? form : {}
  return JSON.stringify({
    enabled: f.enabled === true,
    to: (typeof f.toText === 'string' ? f.toText.split(/[\s,;，；]+/).filter(Boolean) : []).map(s => s.toLowerCase()),
    from: str(f.from).trim(),
    host: str(f.smtp_host).trim(),
    port: f.smtp_port ?? null,
    security: str(f.smtp_security),
    username: str(f.smtp_username).trim(),
    passwordChanged: str(f.smtp_password) !== '',
  })
}

// ---------- 拦截记录 ----------

export const EVENTS_PAGE_SIZE = 20
export const EVENTS_MAX_PAGE_SIZE = 50
// 内存中最多保留的记录数（防止一直点「加载更多」拖慢页面）
export const EVENTS_MEMORY_LIMIT = 500
const EVENT_TEXT_MAX = 2000
const MAIL_ERROR_MAX = 200

const MAIL_STATUS_META = Object.freeze({
  pending: Object.freeze({ label: '待发送', type: 'warning' }),
  sent: Object.freeze({ label: '已发送', type: 'success' }),
  failed: Object.freeze({ label: '发送失败', type: 'danger' }),
  disabled: Object.freeze({ label: '未开启', type: 'info' }),
  silenced: Object.freeze({ label: '已拉黑不发送', type: 'info' }),
})

export function mailStatusMeta(status) {
  return typeof status === 'string' && hasOwn(MAIL_STATUS_META, status)
    ? MAIL_STATUS_META[status]
    : { label: '未知', type: 'info' }
}

// 记录 id 为服务端自增正整数（int64）；字符串形式只接受十进制数字
export function isValidEventCursor(id) {
  if (typeof id === 'number') return Number.isSafeInteger(id) && id > 0
  return typeof id === 'string' && /^[1-9]\d{0,18}$/.test(id)
}

// GET /api/admin/hall/filter/events 的查询参数（limit 夹紧到 1–50；游标格式不对直接拒绝）
export function buildEventsQuery({ before = '', limit = EVENTS_PAGE_SIZE } = {}) {
  const n = Math.floor(Number(limit))
  const params = { limit: Number.isFinite(n) ? Math.min(EVENTS_MAX_PAGE_SIZE, Math.max(1, n)) : EVENTS_PAGE_SIZE }
  if (before !== '' && before !== null && before !== undefined) {
    if (!isValidEventCursor(before)) return { ok: false, error: '记录游标无效' }
    params.before = String(before)
  }
  return { ok: true, params }
}

function clip(value, max) {
  const s = str(value)
  return s.length > max ? s.slice(0, max) + '…' : s
}

export function normalizeEvent(raw) {
  if (!raw || typeof raw !== 'object' || !isValidEventCursor(raw.id)) return null
  const attempts = Number(raw.mail_attempts)
  return {
    id: raw.id,
    key: String(raw.id),
    createdAt: str(raw.created_at),
    sender: str(raw.sender),
    name: clip(raw.name, 64),
    identity: clip(raw.identity, 64),
    vid: clip(raw.vid, 64),
    categories: Array.from(new Set(stringList(raw.categories))),
    original: clip(raw.original, EVENT_TEXT_MAX),
    masked: clip(raw.masked, EVENT_TEXT_MAX),
    mailStatus: str(raw.mail_status),
    mailAttempts: Number.isInteger(attempts) && attempts > 0 ? attempts : 0,
    mailError: clip(raw.mail_error, MAIL_ERROR_MAX),
  }
}

export function normalizeEventList(list) {
  return Array.isArray(list) ? list.map(normalizeEvent).filter(Boolean) : []
}

// 合并记录（新到旧）：已有记录在前，新页追加在后，按 id 去重
export function mergeEvents(existing, incoming) {
  const out = []
  const seen = new Set()
  for (const e of [...(Array.isArray(existing) ? existing : []), ...(Array.isArray(incoming) ? incoming : [])]) {
    if (!e || seen.has(e.key)) continue
    seen.add(e.key)
    out.push(e)
  }
  return out
}

export function oldestEventCursor(list) {
  const last = Array.isArray(list) && list.length ? list[list.length - 1] : null
  return last && isValidEventCursor(last.id) ? last.id : ''
}
