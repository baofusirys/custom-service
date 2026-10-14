#!/usr/bin/env node
// 管理后台联调用本机代理（只监听 127.0.0.1）：/admin/ 提供构建产物，/api /ws /files 转发到本机测试后端。
// 仅测试环境使用：把 Origin 改写为测试后端白名单内的来源，生产由 nginx 同域反代，无需此脚本。
import http from 'node:http';
import net from 'node:net';
import { readFile, stat } from 'node:fs/promises';
import { join, normalize, extname } from 'node:path';

const DIST = process.argv[2];
const PORT = Number(process.argv[3] || 18090);
const WIDGET = process.argv[4] || '';   // 可选：访客端 widget/public 目录，挂在 /widget/
const UP = { host: '127.0.0.1', port: 18080 };
const ORIGIN = 'http://127.0.0.1:18080';
const MIME = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.css': 'text/css; charset=utf-8',
  '.svg': 'image/svg+xml', '.png': 'image/png', '.ico': 'image/x-icon', '.woff2': 'font/woff2', '.json': 'application/json', '.mp3': 'audio/mpeg', '.wav': 'audio/wav' };

function upstreamHeaders(h) {
  const out = { ...h, host: UP.host + ':' + UP.port };
  if (out.origin) out.origin = ORIGIN;
  if (out.referer) out.referer = ORIGIN + '/admin/';
  return out;
}

const server = http.createServer(async (req, res) => {
  if (/^\/(api|files|ws)\//.test(req.url)) {
    const p = http.request({ ...UP, method: req.method, path: req.url, headers: upstreamHeaders(req.headers) }, (r) => {
      res.writeHead(r.statusCode || 502, r.headers);
      r.pipe(res);
    });
    p.on('error', () => { res.writeHead(502); res.end('bad gateway'); });
    req.pipe(p);
    return;
  }
  // 生产中访客端页面以根路径引用脚本（如 /chat.js），这里同样从 widget 目录提供
  const rootWidgetFile = WIDGET && !req.url.startsWith('/admin') && !req.url.startsWith('/widget/') &&
    await stat(join(WIDGET, decodeURIComponent(req.url.split('?')[0]).replace(/^\//, ''))).then((s) => s.isFile()).catch(() => false);
  if (rootWidgetFile) req.url = '/widget' + req.url;
  const isWidget = WIDGET && req.url.startsWith('/widget/');
  if (!isWidget && !req.url.startsWith('/admin')) { res.writeHead(302, { Location: '/admin/' }); res.end(); return; }
  const root = isWidget ? WIDGET : DIST;
  const rel = decodeURIComponent(req.url.split('?')[0].replace(isWidget ? /^\/widget\/?/ : /^\/admin\/?/, ''));
  let file = normalize(join(root, rel));
  if (!file.startsWith(normalize(root))) { res.writeHead(400); res.end(); return; }
  try { if (!(await stat(file)).isFile()) throw new Error('dir'); } catch {
    if (isWidget) { res.writeHead(404); res.end(); return; }
    file = join(DIST, 'index.html');
  }
  try {
    const body = await readFile(file);
    res.writeHead(200, { 'Content-Type': MIME[extname(file)] || 'application/octet-stream', 'Cache-Control': 'no-store' });
    res.end(body);
  } catch { res.writeHead(404); res.end(); }
});

server.on('upgrade', (req, socket, head) => {
  const up = net.connect(UP.port, UP.host, () => {
    const h = upstreamHeaders(req.headers);
    let s = req.method + ' ' + req.url + ' HTTP/1.1\r\n';
    for (const [k, v] of Object.entries(h)) {
      for (const one of (Array.isArray(v) ? v : [v])) s += k + ': ' + one + '\r\n';
    }
    up.write(s + '\r\n');
    if (head && head.length) up.write(head);
    up.pipe(socket);
    socket.pipe(up);
  });
  up.on('error', () => socket.destroy());
  socket.on('error', () => up.destroy());
});

server.listen(PORT, '127.0.0.1', () => console.log('admin e2e proxy on http://127.0.0.1:' + PORT + '/admin/'));
