import test from 'node:test'
import assert from 'node:assert/strict'
import { ConnectionErrorReason, DisconnectReason } from 'livekit-client'

import {
  CONNECTION_ERROR_REASON,
  DISCONNECT_REASON,
  HALL_ERROR_CODES,
  connectErrorMessage,
  disconnectReasonMessage,
  extractHallError,
  formatRetryAfter,
  hallErrorCode,
  hallErrorMessage,
  mediaErrorMessage,
  wsErrorText,
} from '../src/modules/hallErrors.js'

const axiosErr = (status, data) => ({ isAxiosError: true, response: { status, data } })

test('契约第 8 节全部错误码都有中文提示', () => {
  const expected = {
    40371: '大厅已关闭',
    40372: '移出',
    40374: '无法开麦',
    40375: '视频功能未开启',
    40973: '开麦人数已满',
    40976: '摄像头人数已满',
    40977: '大厅人数已满',
    42910: '过于频繁',
  }
  for (const [code, text] of Object.entries(expected)) {
    const msg = hallErrorMessage(axiosErr(403, { code: Number(code), msg: 'forbidden' }))
    assert.ok(msg.includes(text), code + ' => ' + msg)
    assert.match(msg, /[\u4e00-\u9fa5]/)
  }
})

test('[123] 契约第 10.5 节错误码（40377/40378）有中文提示', () => {
  assert.equal(HALL_ERROR_CODES.VOICE_DISABLED, 40377)
  assert.equal(HALL_ERROR_CODES.CHAT_DISABLED, 40378)
  assert.match(hallErrorMessage(axiosErr(403, { code: 40377, msg: 'voice disabled' })), /大厅语音已暂停/)
  assert.match(hallErrorMessage(axiosErr(403, { code: 40378, msg: 'chat disabled' })), /大厅文字聊天已暂停/)
  // http 拦截器 reject 的业务体形态
  assert.match(hallErrorMessage({ code: 40378, msg: '大厅聊天已关闭' }), /大厅文字聊天已暂停/)
  assert.equal(hallErrorCode({ code: 40377 }), 40377)
})

test('契约补充错误码（40059/40060/40376/40477/50370/50050）有中文提示', () => {
  assert.equal(hallErrorMessage(axiosErr(403, { code: 40376, msg: 'x' })), '不能对该对象执行此操作')
  assert.equal(hallErrorMessage(axiosErr(404, { code: 40477, msg: 'x' })), '该大厅消息不存在或已被删除')
  assert.equal(hallErrorMessage(axiosErr(503, { code: 50370, msg: 'x' })), '语音服务暂不可用，请稍后重试')
  assert.match(hallErrorMessage(axiosErr(400, { code: 40059 })), /大厅设置不合法/)
  assert.match(hallErrorMessage(axiosErr(400, { code: 40060 })), /参数错误/)
  assert.match(hallErrorMessage(axiosErr(500, { code: 50050 })), /服务器错误/)
  assert.equal(HALL_ERROR_CODES.LIVEKIT_UNAVAILABLE, 50370)
})

test('契约第 9.4 节错误码（40062/40063/50271）：带上服务端具体原因，否则用通用中文提示', () => {
  assert.equal(HALL_ERROR_CODES.FILTER_INVALID, 40062)
  assert.equal(HALL_ERROR_CODES.ALERT_MAIL_INVALID, 40063)
  assert.equal(HALL_ERROR_CODES.ALERT_MAIL_SEND_FAILED, 50271)
  assert.equal(hallErrorMessage(axiosErr(400, { code: 40062, msg: '自定义屏蔽词第 3 条超过 32 个字' })), '敏感词设置不合法：自定义屏蔽词第 3 条超过 32 个字')
  assert.equal(hallErrorMessage(axiosErr(400, { code: 40062 })), '敏感词设置不合法，请检查后重新保存')
  assert.equal(hallErrorMessage(axiosErr(400, { code: 40063, msg: '邮件设置不合法：收件邮箱最多 5 个' })), '邮件设置不合法：收件邮箱最多 5 个')
  assert.equal(hallErrorMessage(axiosErr(400, { code: 40063, msg: 'invalid smtp_port' })), '邮件设置不合法，请检查后重新保存')
  assert.equal(hallErrorMessage(axiosErr(502, { code: 50271, msg: '认证失败' })), '测试邮件发送失败：认证失败')
  assert.equal(hallErrorMessage(axiosErr(502, { code: 50271, msg: '535 Error: authentication failed' })), '测试邮件发送失败，请检查邮件设置后重试')
  assert.equal(hallErrorMessage({ code: 50271, msg: '连接失败' }), '测试邮件发送失败：连接失败')
  assert.equal(hallErrorMessage(axiosErr(502, { code: 50271, msg: '错'.repeat(300) })), '测试邮件发送失败，请检查邮件设置后重试')
})

test('40372 附带剩余禁入时长', () => {
  const msg = hallErrorMessage(axiosErr(403, { code: 40372, msg: 'x', data: { retry_after: 1800 } }))
  assert.equal(msg, '你已被移出大厅，暂时无法进入（约 30 分钟后可再进入）')
  assert.equal(formatRetryAfter(45), '45 秒')
  assert.equal(formatRetryAfter(61), '2 分钟')
  assert.equal(formatRetryAfter(3600), '1 小时')
  assert.equal(formatRetryAfter(5400), '1 小时 30 分钟')
  assert.equal(formatRetryAfter(-1), '')
  assert.equal(formatRetryAfter('abc'), '')
})

test('拦截器 reject 的业务体、网络错误、超时、5xx 都能映射为中文', () => {
  assert.equal(hallErrorMessage({ code: 40977, msg: 'full' }), '大厅人数已满')
  assert.equal(hallErrorCode({ code: 40977 }), 40977)
  assert.equal(hallErrorMessage({ isAxiosError: true, code: 'ERR_NETWORK' }), '网络连接失败，请检查网络后重试')
  assert.equal(hallErrorMessage({ isAxiosError: true, code: 'ECONNABORTED' }), '请求超时，请稍后重试')
  assert.equal(hallErrorMessage(axiosErr(502, '<html>')), '服务器繁忙，请稍后重试')
  assert.equal(hallErrorMessage(axiosErr(429, {})), '操作过于频繁，请稍后再试')
  // axios 的字符串 code 不能被当成业务码
  assert.equal(extractHallError({ code: 'ERR_BAD_REQUEST' }).code, null)
})

test('服务端 msg 仅在为中文且不超长时采纳，英文/超长回落到通用提示', () => {
  assert.equal(hallErrorMessage(axiosErr(400, { code: 40099, msg: '参数错误' })), '参数错误')
  assert.equal(hallErrorMessage(axiosErr(400, { code: 40099, msg: 'sql: no rows' })), '请求参数有误，请刷新页面后重试')
  assert.equal(hallErrorMessage(axiosErr(418, { code: 41899, msg: '错'.repeat(300) }), '兜底'), '兜底')
  assert.equal(hallErrorMessage(null, '兜底'), '兜底')
})

test('媒体设备错误映射', () => {
  assert.match(mediaErrorMessage({ name: 'NotAllowedError' }, 'microphone'), /未授权使用麦克风/)
  assert.match(mediaErrorMessage({ name: 'NotFoundError' }, 'camera'), /未检测到可用的摄像头/)
  assert.match(mediaErrorMessage({ name: 'NotReadableError' }, 'camera'), /被其他程序占用/)
  assert.match(mediaErrorMessage({ status: 403 }, 'camera'), /暂无摄像头发布权限/)
  assert.match(mediaErrorMessage(new Error('failed to publish track, insufficient permissions'), 'microphone'), /暂无麦克风发布权限/)
  assert.match(mediaErrorMessage({}, 'x'), /媒体设备开启失败/)
})

test('断线原因与连接错误常量与 livekit-client 2.23.0 导出值逐一一致', () => {
  for (const [name, value] of Object.entries(DISCONNECT_REASON)) {
    assert.equal(DisconnectReason[name], value, name)
  }
  for (const [name, value] of Object.entries(CONNECTION_ERROR_REASON)) {
    assert.equal(ConnectionErrorReason[name], value, name)
  }
})

test('断线原因与连接错误提示', () => {
  assert.equal(disconnectReasonMessage(DisconnectReason.CLIENT_INITIATED), '')
  assert.match(disconnectReasonMessage(DisconnectReason.DUPLICATE_IDENTITY), /其他页面或设备/)
  assert.equal(disconnectReasonMessage(DisconnectReason.PARTICIPANT_REMOVED), '你已被移出大厅')
  assert.equal(disconnectReasonMessage(DisconnectReason.ROOM_DELETED), '大厅已关闭')
  assert.match(disconnectReasonMessage(999), /连接已断开/)
  assert.equal(connectErrorMessage({ reason: ConnectionErrorReason.Cancelled }), '')
  assert.match(connectErrorMessage({ reason: ConnectionErrorReason.NotAllowed }), /令牌无效/)
  assert.match(connectErrorMessage({ reason: ConnectionErrorReason.ServerUnreachable }), /无法连接媒体服务器/)
  assert.match(connectErrorMessage(new Error('x')), /连接大厅失败/)
})

test('WS 错误文本兜底与截断', () => {
  assert.equal(wsErrorText('内容过长'), '内容过长')
  assert.equal(wsErrorText(''), '操作失败，请稍后重试')
  assert.equal(wsErrorText(123, 'x'), 'x')
  assert.equal(wsErrorText('长'.repeat(300)).length, 201)
})
