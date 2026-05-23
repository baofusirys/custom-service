import 'dart:math';
import 'dart:typed_data';
import 'package:audioplayers/audioplayers.dart';

/// 通知声音库：跟 Web 端 sound.js 完全对齐的 11 种音色。
/// 用 dart 程序合成 PCM + WAV 头，audioplayers 播放 BytesSource。
/// 零外部文件，零体积增加。

class _SoundDef {
  final String label;
  final Uint8List Function() build;
  const _SoundDef(this.label, this.build);
}

const _sampleRate = 44100;

final _player = AudioPlayer()..setReleaseMode(ReleaseMode.stop);
DateTime _lastPlay = DateTime.fromMillisecondsSinceEpoch(0);

/// 播放音色。500ms 内同名音色防抖（避免连发消息时声音叠成噪声）。
Future<void> playSound(String name) async {
  if (name.isEmpty || name == 'none') return;
  final now = DateTime.now();
  if (now.difference(_lastPlay) < const Duration(milliseconds: 500)) return;
  _lastPlay = now;
  final def = _sounds[name];
  if (def == null) return;
  try {
    final wav = def.build();
    await _player.stop();
    await _player.play(BytesSource(wav));
  } catch (_) {
    // 静默失败：手机静音模式 / 音频设备未就绪
  }
}

List<Map<String, String>> listSounds() {
  return _sounds.entries.map((e) => {'value': e.key, 'label': e.value.label}).toList();
}

// ============================================================
// 11 种音色定义
// ============================================================

final Map<String, _SoundDef> _sounds = {
  'classic': _SoundDef('经典', () => _tone(880, 0.18, 0.35)),
  'chime': _SoundDef('清脆', () => _sequence([
        [1320, 0.08],
        [990, 0.18],
      ], 'sine', 0.35)),
  'ding': _SoundDef('叮咚', () => _sequence([
        [587, 0.1],
        [784, 0.18],
      ], 'sine', 0.4)),
  'soft': _SoundDef('柔和', () => _tone(523, 0.35, 0.25, type: 'triangle')),
  'alert': _SoundDef('提醒', () => _sequence([
        [700, 0.06], [0, 0.04], [700, 0.06], [0, 0.04], [700, 0.1],
      ], 'sine', 0.3)),
  // ===== 响亮长音色 =====
  'bell': _SoundDef('铃声 (长)', () => _layered([
        [1046.5, 0.5, 1.2],
        [2093.0, 0.18, 0.85],
      ])),
  'doorbell': _SoundDef('门铃 (长)', () => _sequence([
        [659.25, 0.25],
        [523.25, 0.6],
      ], 'sine', 0.5)),
  'trill': _SoundDef('颤音 (急)', () => _sequence([
        [880, 0.08], [1100, 0.08], [880, 0.08], [1100, 0.08],
        [880, 0.08], [1100, 0.08], [880, 0.22],
      ], 'sine', 0.45)),
  'fanfare': _SoundDef('号角', () => _sequence([
        [523.25, 0.12], [659.25, 0.12], [783.99, 0.12], [1046.5, 0.4],
      ], 'sine', 0.5)),
  'chord': _SoundDef('和弦', () => _layered([
        [523.25, 0.28, 0.8],
        [659.25, 0.28, 0.8],
        [783.99, 0.28, 0.8],
      ])),
  'none': _SoundDef('静音', () => _wavify([])),
};

// ============================================================
// PCM 合成 + WAV 头封装
// ============================================================

/// 单频音，10ms 渐入 + 指数衰减包络
Uint8List _tone(double freq, double durSec, double vol, {String type = 'sine'}) {
  final total = (_sampleRate * (durSec + 0.02)).round();
  final samples = List<double>.filled(total, 0);
  for (var i = 0; i < total; i++) {
    final t = i / _sampleRate;
    if (t >= durSec) break;
    final env = _envelope(t, durSec);
    samples[i] = vol * env * _wave(freq, t, type);
  }
  return _wavify(samples);
}

/// 顺序播放多个音符（含静音段）
Uint8List _sequence(List<List<double>> notes, String type, double vol) {
  final samples = <double>[];
  for (final n in notes) {
    final freq = n[0];
    final dur = n[1];
    final segLen = (_sampleRate * dur).round();
    if (freq <= 0) {
      samples.addAll(List<double>.filled(segLen, 0));
      continue;
    }
    for (var i = 0; i < segLen; i++) {
      final t = i / _sampleRate;
      final env = _envelope(t, dur);
      samples.add(vol * env * _wave(freq, t, type));
    }
  }
  return _wavify(samples);
}

/// 多层同时播放（和弦 / 谐波叠加）；layers: [[freq, vol, dur], ...]
Uint8List _layered(List<List<double>> layers) {
  var maxDur = 0.0;
  for (final l in layers) {
    if (l[2] > maxDur) maxDur = l[2];
  }
  final total = (_sampleRate * (maxDur + 0.02)).round();
  final samples = List<double>.filled(total, 0);
  for (final l in layers) {
    final freq = l[0];
    final vol = l[1];
    final dur = l[2];
    final segLen = (_sampleRate * dur).round();
    for (var i = 0; i < segLen && i < total; i++) {
      final t = i / _sampleRate;
      final env = _envelope(t, dur);
      samples[i] += vol * env * sin(2 * pi * freq * t);
    }
  }
  // 防 clip
  for (var i = 0; i < samples.length; i++) {
    if (samples[i] > 1) samples[i] = 1;
    if (samples[i] < -1) samples[i] = -1;
  }
  return _wavify(samples);
}

/// 包络：10ms 渐入 + 指数衰减
double _envelope(double t, double durSec) {
  if (t < 0.01) return t / 0.01;
  if (t >= durSec) return 0;
  // 指数衰减，6 倍时间常数后基本归零
  return exp(-6 * (t - 0.01) / durSec);
}

double _wave(double freq, double t, String type) {
  final phase = 2 * pi * freq * t;
  switch (type) {
    case 'triangle':
      return 2 / pi * asin(sin(phase));
    case 'square':
      return sin(phase) >= 0 ? 1 : -1;
    case 'sine':
    default:
      return sin(phase);
  }
}

/// 把 PCM 浮点采样 (-1 ~ 1) 包装成 16-bit 单声道 WAV
Uint8List _wavify(List<double> samples) {
  final n = samples.length;
  final dataSize = n * 2;
  final fileSize = 44 + dataSize;
  final buf = ByteData(fileSize);
  // RIFF header: "RIFF" + size + "WAVE"
  _writeAscii(buf, 0, 'RIFF');
  buf.setUint32(4, fileSize - 8, Endian.little);
  _writeAscii(buf, 8, 'WAVE');
  // fmt chunk
  _writeAscii(buf, 12, 'fmt ');
  buf.setUint32(16, 16, Endian.little);     // chunk size
  buf.setUint16(20, 1, Endian.little);      // PCM format
  buf.setUint16(22, 1, Endian.little);      // mono
  buf.setUint32(24, _sampleRate, Endian.little);
  buf.setUint32(28, _sampleRate * 2, Endian.little); // byte rate
  buf.setUint16(32, 2, Endian.little);      // block align
  buf.setUint16(34, 16, Endian.little);     // bits per sample
  // data chunk
  _writeAscii(buf, 36, 'data');
  buf.setUint32(40, dataSize, Endian.little);
  for (var i = 0; i < n; i++) {
    var v = samples[i];
    if (v > 1) v = 1;
    if (v < -1) v = -1;
    final s = (v * 32767).round();
    buf.setInt16(44 + i * 2, s, Endian.little);
  }
  return buf.buffer.asUint8List();
}

void _writeAscii(ByteData buf, int offset, String s) {
  for (var i = 0; i < s.length; i++) {
    buf.setUint8(offset + i, s.codeUnitAt(i));
  }
}
