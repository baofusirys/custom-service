#!/usr/bin/env node
// [115] 会话列表新旧 SQL 只读对比 / EXPLAIN 生成器（只输出 SELECT / EXPLAIN 语句，不含任何写操作）。
// 旧 SQL = 0.8.0 store.go listVisitorAggregated / countVisibleVisitors 原文；新 SQL = 0.8.1 conversation_list.go 原文。
// 对比结果只输出数量、位置序号、是否一致，不输出任何访客 ID / 内容。
// 用法：node list_compare_sql.mjs compare [--tag 标签]      → 输出 all / contacted（及可选标签）的全量顺序 + 计数对比 SQL
//       node list_compare_sql.mjs explain-new|explain-old [--contacted] [--tag X] [--limit 50] [--offset 0]
//       node list_compare_sql.mjs new-rep|new-preview          → 第 2 阶段 SQL，IN 列表用 __IDS__ 占位（由调用方替换）
// 也可作为模块 import 使用（导出各 SQL 生成函数）。
import { pathToFileURL } from 'node:url';

export const sqlStr = (s) => "'" + String(s).replace(/\\/g, '\\\\').replace(/'/g, "''") + "'";
const int = (n) => { const v = Number(n); if (!Number.isInteger(v) || v < 0) throw new Error('bad int ' + n); return v; };

// ===== 旧实现（0.8.0 原文） =====
export function oldList({ contacted = false, tag = '', limit = 50, offset = 0 } = {}) {
  let filter = 'WHERE c.deleted_at IS NULL';
  if (contacted) filter += ' AND c.visitor_id IN (SELECT DISTINCT visitor_id FROM conversations WHERE visitor_engaged = 1 AND deleted_at IS NULL)';
  const tagFilter = tag ? ' AND EXISTS (SELECT 1 FROM visitor_tags vt WHERE vt.visitor_id = t.visitor_id AND vt.tag = ' + sqlStr(tag) + ')' : '';
  return `
SELECT t.id, t.visitor_id, t.agent_id, t.unread_agent, t.status, t.started_at, t.updated_at,
       v.identifier, v.country, v.city, v.last_page, v.referer, v.ip_cipher,
       EXISTS(SELECT 1 FROM conversations c3 WHERE c3.visitor_id = t.visitor_id AND c3.visitor_engaged = 1 AND c3.deleted_at IS NULL) AS contacted,
       COALESCE(p.note, ''), p.pinned_at IS NOT NULL, COALESCE(p.marked_unread, 0)
FROM (
  SELECT c.id, c.visitor_id, c.agent_id, c.unread_agent, c.status, c.started_at, c.updated_at,
         ROW_NUMBER() OVER (PARTITION BY c.visitor_id ORDER BY (c.status = 'open') DESC, c.updated_at DESC, c.started_at DESC) AS rn
  FROM conversations c
  ${filter}
) t
JOIN visitors v ON v.id = t.visitor_id
LEFT JOIN visitor_profiles p ON p.visitor_id = t.visitor_id
WHERE t.rn = 1 AND p.blocked_at IS NULL${tagFilter}
ORDER BY (p.pinned_at IS NOT NULL) DESC, p.pinned_at DESC, t.updated_at DESC
LIMIT ${int(limit)} OFFSET ${int(offset)}`;
}

export function oldCount({ contacted = false, tag = '' } = {}) {
  let q = `
SELECT COUNT(DISTINCT c.visitor_id) FROM conversations c
LEFT JOIN visitor_profiles p ON p.visitor_id = c.visitor_id
WHERE c.deleted_at IS NULL AND p.blocked_at IS NULL`;
  if (contacted) q += ' AND c.visitor_engaged = 1';
  if (tag) q += ' AND EXISTS (SELECT 1 FROM visitor_tags vt WHERE vt.visitor_id = c.visitor_id AND vt.tag = ' + sqlStr(tag) + ')';
  return q;
}

// ===== 新实现（0.8.1 conversation_list.go 原文，? 以字面量代入） =====
function groupSQL(contacted, withLastActive) {
  let s = '\n  SELECT c.visitor_id';
  if (withLastActive) s += ",\n         COALESCE(MAX(CASE WHEN c.status = 'open' THEN c.updated_at END), MAX(c.updated_at)) AS last_active";
  s += '\n  FROM conversations c IGNORE INDEX (idx_conv_deleted)\n  WHERE c.deleted_at IS NULL\n  GROUP BY c.visitor_id';
  if (contacted) s += '\n  HAVING MAX(c.visitor_engaged) = 1';
  return s;
}
const tagExists = (tag) => '\n  AND EXISTS (SELECT 1 FROM visitor_tags vt WHERE vt.visitor_id = a.visitor_id AND vt.tag = ' + sqlStr(tag) + ')';

// selectExtra：对比时额外带出排序键（不影响执行计划）；生产代码只 SELECT a.visitor_id。
export function newPage({ contacted = false, tag = '', limit = 50, offset = 0, selectExtra = false } = {}) {
  let s = '\nSELECT a.visitor_id' + (selectExtra ? ', a.last_active, (p.pinned_at IS NOT NULL) AS pinned' : '') + '\nFROM (' + groupSQL(contacted, true);
  s += '\n) a\nJOIN visitors v ON v.id = a.visitor_id\nLEFT JOIN visitor_profiles p ON p.visitor_id = a.visitor_id\nWHERE p.blocked_at IS NULL';
  if (tag) s += tagExists(tag);
  s += '\nORDER BY (p.pinned_at IS NOT NULL) DESC, p.pinned_at DESC, a.last_active DESC, a.visitor_id DESC\nLIMIT ' + int(limit) + ' OFFSET ' + int(offset);
  return s;
}

export function newCount({ contacted = false, tag = '' } = {}) {
  let s = '\nSELECT COUNT(*)\nFROM (' + groupSQL(contacted, false);
  s += '\n) a\nLEFT JOIN visitor_profiles p ON p.visitor_id = a.visitor_id\nWHERE p.blocked_at IS NULL';
  if (tag) s += tagExists(tag);
  return s;
}

export function newRep(ids = '__IDS__') {
  return `
SELECT t.id, t.visitor_id, t.agent_id, t.unread_agent, t.status, t.started_at, t.updated_at, (t.engaged = 1) AS contacted,
       v.identifier, v.country, v.city, v.last_page, v.referer, v.ip_cipher,
       COALESCE(p.note, ''), p.pinned_at IS NOT NULL, COALESCE(p.marked_unread, 0)
FROM (
  SELECT c.id, c.visitor_id, c.agent_id, c.unread_agent, c.status, c.started_at, c.updated_at,
         MAX(c.visitor_engaged) OVER (PARTITION BY c.visitor_id) AS engaged,
         ROW_NUMBER() OVER (PARTITION BY c.visitor_id ORDER BY (c.status = 'open') DESC, c.updated_at DESC, c.started_at DESC, c.id DESC) AS rn
  FROM conversations c IGNORE INDEX (idx_conv_deleted)
  WHERE c.deleted_at IS NULL AND c.visitor_id IN (${ids})
) t
JOIN visitors v ON v.id = t.visitor_id
LEFT JOIN visitor_profiles p ON p.visitor_id = t.visitor_id
WHERE t.rn = 1`;
}

export function newPreview(ids = '__IDS__') {
  return `
SELECT c.id, lm.sender, lm.content, lm.media_kind, lm.created_at, lm.recalled
FROM conversations c,
LATERAL (
  SELECT m.sender, LEFT(m.content, 51) AS content, m.media_kind, m.created_at, m.recalled_at IS NOT NULL AS recalled
  FROM messages m
  WHERE m.conv_id = c.id AND NOT (m.sender = 'sys' AND m.sender_ref LIKE 'page:%')
  ORDER BY m.created_at DESC
  LIMIT 1
) lm
WHERE c.id IN (${ids})`;
}

// ===== 对比：全量顺序逐位比较（旧/新各一次），前 200 名单独统计；只输出数量与位置序号 =====
export function compareSQL({ contacted = false, tag = '', label = '' } = {}) {
  const FULL = 1000000;
  const name = label || (contacted ? 'contacted' : 'all') + (tag ? '+tag' : '');
  return `
WITH o AS (
  SELECT x.visitor_id, x.updated_at AS k, x.\`p.pinned_at IS NOT NULL\` AS kp, ROW_NUMBER() OVER () AS pos
  FROM (${oldList({ contacted, tag, limit: FULL, offset: 0 })}
  ) x
), n AS (
  SELECT y.visitor_id, y.last_active AS k, y.pinned AS kp, ROW_NUMBER() OVER () AS pos
  FROM (${newPage({ contacted, tag, limit: FULL, offset: 0, selectExtra: true })}
  ) y
)
SELECT ${sqlStr(name)} AS mode,
  (SELECT COUNT(*) FROM o) AS old_rows,
  (SELECT COUNT(*) FROM n) AS new_rows,
  SUM(o.pos <= 200) AS top200_rows,
  SUM(o.pos <= 200 AND o.visitor_id = n.visitor_id) AS top200_same_visitor,
  SUM(o.pos <= 200 AND o.k = n.k AND o.kp = n.kp) AS top200_same_sortkey,
  COALESCE(GROUP_CONCAT(IF(o.pos <= 200 AND o.visitor_id <> n.visitor_id, o.pos, NULL) ORDER BY o.pos), '-') AS top200_diff_pos,
  SUM(o.visitor_id = n.visitor_id) AS full_same_visitor,
  SUM(o.k = n.k AND o.kp = n.kp) AS full_same_sortkey,
  SUM(o.visitor_id <> n.visitor_id) AS full_diff_visitor_pos,
  SUM(o.visitor_id <> n.visitor_id AND NOT (o.k = n.k AND o.kp = n.kp)) AS full_diff_with_different_key,
  (SELECT COUNT(*) FROM o WHERE o.visitor_id NOT IN (SELECT visitor_id FROM n)) AS old_not_in_new,
  (SELECT COUNT(*) FROM n WHERE n.visitor_id NOT IN (SELECT visitor_id FROM o)) AS new_not_in_old,
  (${oldCount({ contacted, tag }).trim()}) AS old_count,
  (${newCount({ contacted, tag }).trim()}) AS new_count
FROM o JOIN n ON n.pos = o.pos;`;
}

function parseArgs(argv) {
  const o = { contacted: false, tag: '', limit: 50, offset: 0 };
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--contacted') o.contacted = true;
    else if (argv[i] === '--tag') o.tag = argv[++i] || '';
    else if (argv[i] === '--limit') o.limit = int(argv[++i]);
    else if (argv[i] === '--offset') o.offset = int(argv[++i]);
  }
  return o;
}

if (import.meta.url === pathToFileURL(process.argv[1] || '').href) {
  const [cmd, ...rest] = process.argv.slice(2);
  const a = parseArgs(rest);
  const out = [];
  if (cmd === 'compare') {
    out.push(compareSQL({ contacted: false }), compareSQL({ contacted: true }));
    if (a.tag) out.push(compareSQL({ contacted: false, tag: a.tag }), compareSQL({ contacted: true, tag: a.tag }));
  } else if (cmd === 'explain-new') {
    out.push('EXPLAIN ANALYZE ' + newPage(a) + ';', 'EXPLAIN ANALYZE ' + newCount(a) + ';');
  } else if (cmd === 'explain-old') {
    out.push('EXPLAIN ANALYZE ' + oldList(a) + ';', 'EXPLAIN ANALYZE ' + oldCount(a) + ';');
  } else if (cmd === 'new-rep') {
    out.push(newRep() + ';');
  } else if (cmd === 'new-preview') {
    out.push(newPreview() + ';');
  } else if (cmd === 'new-page') {
    out.push(newPage(a) + ';');
  } else {
    console.error('usage: node list_compare_sql.mjs compare|explain-new|explain-old|new-page|new-rep|new-preview [--contacted] [--tag X] [--limit N] [--offset N]');
    process.exit(2);
  }
  process.stdout.write(out.join('\n') + '\n');
}
