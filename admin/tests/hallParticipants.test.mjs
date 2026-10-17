import test from 'node:test'
import assert from 'node:assert/strict'

import {
  HallRoster,
  countRows,
  filterRows,
  isValidHallIdentity,
  normalizeServerParticipant,
  parseParticipantMetadata,
  roleFromIdentity,
  rowActions,
  sanitizeDisplayName,
} from '../src/modules/hallParticipants.js'

const V1 = 'v_0123456789abcdef0123'
const V2 = 'v_aaaaaaaaaaaaaaaaaaaa'
const A1 = 'a_7'
const A2 = 'a_9'
const live = (identity, extra = {}) => ({ identity, name: '', metadata: '', micOn: false, cameraOn: false, speaking: false, isLocal: false, joinedAt: 0, ...extra })
const srv = (identity, extra = {}) => ({ identity, name: '', role: identity.startsWith('v_') ? 'visitor' : 'agent', mic_on: false, camera_on: false, joined_at: '2026-10-17T10:00:00+08:00', blocked: false, ...extra })

test('identity 校验：访客 v_+20 位小写十六进制，客服 a_+安全 ID', () => {
  assert.equal(isValidHallIdentity(V1), true)
  assert.equal(isValidHallIdentity(A1), true)
  assert.equal(isValidHallIdentity('v_0123'), false)
  assert.equal(isValidHallIdentity('v_0123456789ABCDEF0123'), false)
  assert.equal(isValidHallIdentity('a_../x'), false)
  assert.equal(isValidHallIdentity('a_abc'), false) // 契约补充：客服为 a_ + 1–19 位数字
  assert.equal(isValidHallIdentity('a_' + '1'.repeat(20)), false)
  assert.equal(isValidHallIdentity('x_1'), false)
  assert.equal(roleFromIdentity(V1), 'visitor')
  assert.equal(roleFromIdentity(A1), 'agent')
})

test('metadata 解析只接受服务端签发的 role/code，异常输入不抛错', () => {
  assert.deepEqual(parseParticipantMetadata('{"role":"visitor","code":"0421"}'), { role: 'visitor', code: '0421' })
  assert.deepEqual(parseParticipantMetadata('{"role":"admin","code":"12345"}'), { role: '', code: '' })
  assert.deepEqual(parseParticipantMetadata('{bad'), { role: '', code: '' })
  assert.deepEqual(parseParticipantMetadata('x'.repeat(2000)), { role: '', code: '' })
  assert.deepEqual(parseParticipantMetadata(null), { role: '', code: '' })
})

test('显示名清洗：去控制字符与双向文本控制符、限长 64', () => {
  assert.equal(sanitizeDisplayName('  客服·小王\u202E  '), '客服·小王')
  assert.equal(sanitizeDisplayName('', '访客'), '访客')
  assert.equal(Array.from(sanitizeDisplayName('名'.repeat(100))).length, 65)
})

test('服务端参与者：角色与身份前缀不一致视为异常；vid 只在访客上保留', () => {
  assert.equal(normalizeServerParticipant(srv(V1, { role: 'agent' })), null)
  assert.equal(normalizeServerParticipant({ identity: 'bad' }), null)
  const p = normalizeServerParticipant(srv(V1, { vid: 'vid_abc-1', name: '访客 0421', blocked: 'true' }))
  assert.equal(p.vid, 'vid_abc-1')
  assert.equal(p.blocked, true)
  assert.equal(normalizeServerParticipant(srv(V1, { vid: '../x' })).vid, '')
  assert.equal(normalizeServerParticipant(srv(A1, { vid: 'vid_abc', agent_id: 7 })).vid, '')
  assert.equal(normalizeServerParticipant(srv(A1, { agent_id: 7 })).agentId, '7')
})

test('未连接时完全使用服务端状态与服务端计数', () => {
  const r = new HallRoster()
  r.replaceServer([srv(V1, { vid: 'vid1', mic_on: true }), srv(A1)], { participants: 2, speakers: 1, cameras: 0 })
  const rows = r.rows()
  assert.deepEqual(rows.map(x => x.identity), [A1, V1])
  assert.equal(rows[1].micOn, true)
  assert.equal(rows[1].speaking, false)
  assert.deepEqual(r.counts(), { participants: 2, speakers: 1, visitorSpeakers: 1, cameras: 0 })
})

test('已连接时以 LiveKit 为准：在场名单、麦克风、说话；服务端补 vid 与拉黑', () => {
  const r = new HallRoster()
  r.replaceServer([srv(V1, { vid: 'vid1', mic_on: false, blocked: true }), srv(V2, { vid: 'vid2' })])
  r.setConnected(true, A1)
  r.upsertLive(live(A1, { name: '客服·我', isLocal: true, micOn: true }))
  assert.equal(r.upsertLive(live(V1, { name: '访客 0001', micOn: true, metadata: '{"role":"visitor","code":"0001"}' })), false)
  // 服务端还不知道的新身份：返回 true 供调用方提前校准
  assert.equal(r.upsertLive(live('v_bbbbbbbbbbbbbbbbbbbb', { name: '访客 0002' })), true)
  r.setSpeakers([V1, 'bad'])
  const rows = r.rows()
  // V2 只在服务端（LiveKit 已离开）→ 不显示
  assert.deepEqual(rows.map(x => x.identity), [A1, V1, 'v_bbbbbbbbbbbbbbbbbbbb'])
  const v1 = rows[1]
  assert.equal(v1.micOn, true)
  assert.equal(v1.speaking, true)
  assert.equal(v1.vid, 'vid1')
  assert.equal(v1.blocked, true)
  assert.equal(rows[0].isSelf, true)
  assert.equal(rows[2].known, false)
  assert.deepEqual(r.counts(), { participants: 3, speakers: 2, visitorSpeakers: 1, cameras: 0 })
  r.removeLive(V1)
  assert.equal(r.get(V1), null)
  r.setConnected(false)
  assert.deepEqual(r.rows().map(x => x.identity), [V1, V2])
})

test('排序：客服在前、自己在客服首位，其余按进入时间', () => {
  const r = new HallRoster()
  r.setConnected(true, A2)
  r.upsertLive(live(V1, { joinedAt: 1000 }))
  r.upsertLive(live(A1, { joinedAt: 500 }))
  r.upsertLive(live(V2, { joinedAt: 900 }))
  r.upsertLive(live(A2, { joinedAt: 2000, isLocal: true }))
  assert.deepEqual(r.rows().map(x => x.identity), [A2, A1, V2, V1])
})

test('硬上限防异常数据撑爆内存', () => {
  const r = new HallRoster({ hardLimit: 3 })
  r.setConnected(true, A1)
  for (let i = 0; i < 10; i++) r.upsertLive(live('v_' + String(i).padStart(20, '0')))
  assert.equal(r.rows().length, 3)
  r.replaceServer(Array.from({ length: 10 }, (_, i) => srv('v_' + String(i).padStart(20, '1'))))
  assert.equal(r.server.size, 3)
})

test('操作可用性：普通客服只能操作访客；管理员可操作其他客服；有 vid 才能拉黑/私聊；不能操作自己', () => {
  assert.deepEqual(rowActions({ role: 'visitor', micOn: true, cameraOn: true, vid: 'vid1', blocked: false }),
    { mute: true, cameraOff: true, kick: true, block: true, chat: true })
  assert.deepEqual(rowActions({ role: 'visitor', micOn: false, cameraOn: false, vid: '', blocked: false }),
    { mute: false, cameraOff: false, kick: true, block: false, chat: false })
  assert.equal(rowActions({ role: 'visitor', vid: 'vid1', blocked: true }).block, false)
  assert.deepEqual(rowActions({ role: 'agent', micOn: true, cameraOn: true }),
    { mute: false, cameraOff: false, kick: false, block: false, chat: false })
  assert.deepEqual(rowActions({ role: 'agent', micOn: true, cameraOn: true }, { isAdmin: true }),
    { mute: true, cameraOff: true, kick: true, block: false, chat: false })
  assert.deepEqual(rowActions({ role: 'agent', isSelf: true, cameraOn: true }, { isAdmin: true }),
    { mute: false, cameraOff: false, kick: false, block: false, chat: false })
  // isAdmin 只接受布尔 true，防止把字符串 'false' 当真
  assert.equal(rowActions({ role: 'agent', cameraOn: true }, { isAdmin: 'false' }).cameraOff, false)
  assert.equal(rowActions(null).kick, false)
})

test('按名字筛选与计数', () => {
  const rows = [{ name: '访客 0421', micOn: true, role: 'visitor', cameraOn: true }, { name: '客服·小王', role: 'agent', micOn: true }]
  assert.equal(filterRows(rows, '0421').length, 1)
  assert.equal(filterRows(rows, '  ').length, 2)
  assert.deepEqual(countRows(rows), { participants: 2, speakers: 2, visitorSpeakers: 1, cameras: 1 })
})
