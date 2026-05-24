import 'package:audioplayers/audioplayers.dart';

/// 通知声音库：使用真实录制的 WAV 文件（assets/sounds/）。
///
/// 替换历史：之前用 dart 程序合成 PCM + WAV 头 + BytesSource。问题：
///   - iOS audioplayers 6.x 默认临时文件后缀 .mp3，AVPlayer 当 mp3 解码合成 WAV 失败 (err=-12860)
///   - 加 mimeType: 'audio/wav' 能播但音量偏小（合成幅度只有 0.3-0.5）
///   - 爷爷直接给真实录音 → 一步到位，音质好、音量足、所有端用同一份音色
///
/// 新音色：6 个真实录音 + 静音
///   - agent1/2/3：工作台音色（客服端默认）
///   - visitor1/2/3：访客端音色（访客端默认）
///   - none：静音

class _SoundDef {
  final String label;
  final String? asset; // 相对于 assets/ 的路径；null 表示静音
  const _SoundDef(this.label, this.asset);
}

final Map<String, _SoundDef> _sounds = {
  'agent1':   const _SoundDef('工作台音色 1', 'sounds/agent1.wav'),
  'agent2':   const _SoundDef('工作台音色 2', 'sounds/agent2.wav'),
  'agent3':   const _SoundDef('工作台音色 3', 'sounds/agent3.wav'),
  'visitor1': const _SoundDef('访客端音色 1', 'sounds/visitor1.wav'),
  'visitor2': const _SoundDef('访客端音色 2', 'sounds/visitor2.wav'),
  'visitor3': const _SoundDef('访客端音色 3', 'sounds/visitor3.wav'),
  'none':     const _SoundDef('静音', null),
};

DateTime _lastPlay = DateTime.fromMillisecondsSinceEpoch(0);
bool _audioContextInited = false;

/// iOS / Android 通用通知音上下文：
///   - iOS: AVAudioSessionCategory.playback —— 静音键也能响，关键
///   - Android: usage=notification + content=sonification，跟系统通知同等级
Future<void> _ensureAudioContext() async {
  if (_audioContextInited) return;
  _audioContextInited = true;
  try {
    await AudioPlayer.global.setAudioContext(
      AudioContext(
        iOS: AudioContextIOS(
          category: AVAudioSessionCategory.playback,
          options: const {AVAudioSessionOptions.mixWithOthers},
        ),
        android: const AudioContextAndroid(
          isSpeakerphoneOn: false,
          stayAwake: false,
          contentType: AndroidContentType.sonification,
          usageType: AndroidUsageType.notification,
          audioFocus: AndroidAudioFocus.gainTransientMayDuck,
        ),
      ),
    );
  } catch (_) {
    _audioContextInited = false;
  }
}

/// 播放音色。500ms 内同名音色防抖（避免连发消息时声音叠成噪声）。
/// 每次 new 一个新 AudioPlayer 实例避免 native state machine 复用 bug。
Future<void> playSound(String name) async {
  if (name.isEmpty || name == 'none') return;
  final now = DateTime.now();
  if (now.difference(_lastPlay) < const Duration(milliseconds: 500)) return;
  _lastPlay = now;
  // 老数据库里可能存的是 classic/chime 等旧 key → fallback 到 agent1
  final def = _sounds[name] ?? _sounds['agent1'];
  if (def == null || def.asset == null) return;
  await _ensureAudioContext();
  final p = AudioPlayer();
  try {
    p.onPlayerComplete.listen((_) async {
      try { await p.dispose(); } catch (_) {}
    });
    Future.delayed(const Duration(seconds: 8), () async {
      try { await p.dispose(); } catch (_) {}
    });
    await p.setReleaseMode(ReleaseMode.release);
    await p.setVolume(1.0);
    await p.play(AssetSource(def.asset!));
  } catch (_) {
    try { await p.dispose(); } catch (_) {}
  }
}

List<Map<String, String>> listSounds() {
  return _sounds.entries.map((e) => {'value': e.key, 'label': e.value.label}).toList();
}

// ============= 语音来电铃声（循环播放）[036] =============
// 跟普通 playSound 分离：来电要循环播直到接通/拒绝/超时；普通消息提示音只响一下
// 资源：assets/sounds/voice-ring.mp3（与 admin/widget 统一）
//
// iOS 行为：voice_call_page 进入前台时 _onIncoming 触发 → 播放循环
//           App 在后台时 voice_call 信令到达 hub→APNs 通知系统响（系统音色 4）
//           用户点推送拉 App 起来 → WSS 重连 → buffer 重投 voice_call → _onIncoming 触发 → 播放循环
AudioPlayer? _ringPlayer;

Future<void> playRingLoop() async {
  await _ensureAudioContext();
  // 先停旧的（防御性：如果业务上重复调 play）
  await stopRingLoop();
  final p = AudioPlayer();
  _ringPlayer = p;
  try {
    await p.setReleaseMode(ReleaseMode.loop);
    await p.setVolume(1.0);
    await p.play(AssetSource('sounds/voice-ring.mp3'));
  } catch (_) {
    try { await p.dispose(); } catch (_) {}
    if (identical(_ringPlayer, p)) _ringPlayer = null;
  }
}

Future<void> stopRingLoop() async {
  final p = _ringPlayer;
  if (p == null) return;
  _ringPlayer = null;
  try { await p.stop(); } catch (_) {}
  try { await p.dispose(); } catch (_) {}
}
