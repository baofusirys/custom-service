#!/usr/bin/env node
// [122] 测试环境本机 SMTP 接收器（只监听 127.0.0.1，不加密，仅供隔离测试）：支持 EHLO / AUTH PLAIN / AUTH LOGIN / MAIL / RCPT / DATA / QUIT。
// 收到的邮件以 .eml 写入 ~/cs-testenv/run/smtp-sink/，并在标准输出打印一行摘要（不打印正文）。
// 用法：node smtp_sink.mjs [端口，默认 2525]；账号 qa / qa-pass（仅测试用）。
import net from 'node:net';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const PORT = Number(process.argv[2] || process.env.HALL_E2E_SMTP_PORT || 2525);
const DIR = join(process.env.HOME || '.', 'cs-testenv', 'run', 'smtp-sink');
const USER = 'qa', PASS = 'qa-pass';
mkdirSync(DIR, { recursive: true });
let seq = 0;
const server = net.createServer((sock) => {
  let buf = '', mode = 'cmd', authed = false, from = '', rcpts = [], data = [], loginStep = 0, loginUser = '';
  const w = (s) => sock.write(s + '\r\n');
  w('220 cs-testenv smtp sink');
  sock.on('data', (chunk) => {
    buf += chunk.toString('utf8');
    let i;
    while ((i = buf.indexOf('\r\n')) >= 0) {
      const line = buf.slice(0, i); buf = buf.slice(i + 2);
      if (mode === 'data') {
        if (line === '.') {
          mode = 'cmd';
          const file = join(DIR, Date.now() + '-' + (++seq) + '.eml');
          writeFileSync(file, data.join('\r\n'));
          console.log(JSON.stringify({ event: 'mail', from, rcpts, bytes: data.join('\r\n').length, file }));
          data = []; rcpts = [];
          w('250 queued');
        } else data.push(line.startsWith('..') ? line.slice(1) : line);
        continue;
      }
      if (loginStep === 1) { loginUser = Buffer.from(line, 'base64').toString(); loginStep = 2; w('334 UGFzc3dvcmQ6'); continue; }
      if (loginStep === 2) { loginStep = 0; authed = loginUser === USER && Buffer.from(line, 'base64').toString() === PASS; w(authed ? '235 ok' : '535 auth failed'); continue; }
      const up = line.toUpperCase();
      if (up.startsWith('EHLO') || up.startsWith('HELO')) { w('250-cs-testenv'); w('250 AUTH PLAIN LOGIN'); }
      else if (up.startsWith('AUTH PLAIN ')) { const p = Buffer.from(line.slice(11).trim(), 'base64').toString().split('\0'); authed = p[1] === USER && p[2] === PASS; w(authed ? '235 ok' : '535 auth failed'); }
      else if (up === 'AUTH LOGIN') { loginStep = 1; w('334 VXNlcm5hbWU6'); }
      else if (up.startsWith('MAIL FROM:')) { if (!authed) { w('530 auth required'); continue; } from = line.slice(10).replace(/[<>\s]/g, ''); w('250 ok'); }
      else if (up.startsWith('RCPT TO:')) { rcpts.push(line.slice(8).replace(/[<>\s]/g, '')); w('250 ok'); }
      else if (up === 'DATA') { mode = 'data'; w('354 end with .'); }
      else if (up === 'QUIT') { w('221 bye'); sock.end(); }
      else w('250 ok');
    }
  });
  sock.on('error', () => {});
});
server.listen(PORT, '127.0.0.1', () => console.log(JSON.stringify({ event: 'listening', port: PORT, dir: DIR })));
