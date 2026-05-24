import 'dart:async';
import 'package:flutter/foundation.dart';
import 'package:flutter_webrtc/flutter_webrtc.dart';

/// 语音通话状态机：跟 admin/src/views/Console.vue 完全对齐。
///   - idle：空闲，没在通话
///   - incoming：访客来电，等用户接听/拒绝
///   - accepting：已点接听，等访客发 voice_offer 准备 SDP
///   - talking：通话进行中
///   - ended：通话结束（2.5s 后回 idle，UI 显示「已挂断」之类）
enum VoiceState { idle, incoming, accepting, talking, ended }

/// VoiceController 是单例：被 AppState 持有，处理 WSS 来的 voice_* 信令。
/// 不依赖 BuildContext，UI（voice_call_page.dart）作为 ListenableBuilder 监听刷新。
class VoiceController extends ChangeNotifier {
  static const _iceServers = {
    'iceServers': [
      {'urls': 'stun:stun.l.google.com:19302'}
    ]
  };

  /// AppState 注入：信令发送函数 + 当前 agent ID/昵称（accept 时 broadcast 用）
  void Function(Map<String, dynamic>) sendEnvelope = (_) {};
  String agentId = '';
  String agentNickname = '';

  VoiceState state = VoiceState.idle;
  String statusText = '';
  String callerLabel = '';
  // 通话中扬声器是否开（true=外放扬声器，false=听筒）。默认听筒（更私密）。
  bool speakerOn = false;
  String? _callId;
  String? _callerFrom; // "visitor:vid"
  RTCPeerConnection? _pc;
  MediaStream? _localStream;
  MediaStream? _remoteStream;
  DateTime? _startTs;
  Timer? _timer;

  // 信令分发入口（由 AppState._onEnvelope 调）
  void handleSignal(Map<String, dynamic> env) {
    final type = env['type']?.toString() ?? '';
    switch (type) {
      case 'voice_call':   _onIncoming(env); break;
      case 'voice_taken':  _onTaken(env); break;
      case 'voice_offer':  _onOffer(env); break;
      case 'voice_ice':    _onIce(env); break;
      case 'voice_end':    _onRemoteEnd(env); break;
      // accept/reject/answer 是本端发出，自己一般不收
    }
  }

  void _onIncoming(Map<String, dynamic> env) {
    if (state != VoiceState.idle && state != VoiceState.ended) {
      // 忙线，主动告诉 visitor 拒绝
      sendEnvelope({
        'type': 'voice_reject',
        'to': env['from'],
        'ts': DateTime.now().millisecondsSinceEpoch,
        'extra': {'call_id': (env['extra']?['call_id']), 'reason': 'busy'},
      });
      return;
    }
    _callId = (env['extra'] as Map?)?['call_id']?.toString();
    _callerFrom = env['from']?.toString();
    final vid = (_callerFrom ?? '').split(':').last;
    callerLabel = vid.length >= 6 ? '访客 ${vid.substring(0, 6)}' : '访客';
    state = VoiceState.incoming;
    statusText = '语音来电…';
    _resetTimer(const Duration(seconds: 30), () {
      if (state == VoiceState.incoming) _end('未接听', notify: false);
    });
    notifyListeners();
  }

  void _onTaken(Map<String, dynamic> env) {
    if (state != VoiceState.incoming) return;
    if ((env['extra'] as Map?)?['call_id']?.toString() != _callId) return;
    // 别的客服接了，撤销本端来电
    _cleanup();
    state = VoiceState.idle;
    notifyListeners();
  }

  Future<void> accept() async {
    if (state != VoiceState.incoming) return;
    _stopTimer();
    try {
      _localStream = await navigator.mediaDevices.getUserMedia({
        'audio': true,
        'video': false,
      });
    } catch (e) {
      _end('麦克风失败：$e', notify: false);
      return;
    }
    state = VoiceState.accepting;
    statusText = '已接听，等待对方建立通话…';
    notifyListeners();
    // 通知 visitor 接听 + 广播给其他 agent 撤窗
    sendEnvelope({
      'type': 'voice_accept',
      'to': _callerFrom,
      'ts': DateTime.now().millisecondsSinceEpoch,
      'extra': {'call_id': _callId, 'agent_id': agentId, 'agent_name': agentNickname},
    });
    sendEnvelope({
      'type': 'voice_taken',
      'ts': DateTime.now().millisecondsSinceEpoch,
      'extra': {'call_id': _callId},
    });
  }

  void reject() {
    if (state != VoiceState.incoming) return;
    sendEnvelope({
      'type': 'voice_reject',
      'to': _callerFrom,
      'ts': DateTime.now().millisecondsSinceEpoch,
      'extra': {'call_id': _callId, 'code': 'rejected', 'duration': 0},
    });
    _end('已拒绝', notify: false);
  }

  Future<RTCPeerConnection> _createPC() async {
    final pc = await createPeerConnection(_iceServers);
    pc.onIceCandidate = (cand) {
      if (_callerFrom == null) return;
      sendEnvelope({
        'type': 'voice_ice',
        'to': _callerFrom,
        'ts': DateTime.now().millisecondsSinceEpoch,
        'extra': {
          'call_id': _callId,
          'candidate': cand.candidate,
          'sdpMid': cand.sdpMid,
          'sdpMLineIndex': cand.sdpMLineIndex,
        },
      });
    };
    pc.onTrack = (event) {
      if (event.streams.isNotEmpty) {
        _remoteStream = event.streams.first;
        // flutter_webrtc 在 iOS/Android 自动路由音频到设备扬声器，无需 audio widget
      }
    };
    pc.onConnectionState = (s) {
      if (s == RTCPeerConnectionState.RTCPeerConnectionStateFailed ||
          s == RTCPeerConnectionState.RTCPeerConnectionStateDisconnected) {
        _end('连接中断', notify: true);
      }
    };
    return pc;
  }

  Future<void> _onOffer(Map<String, dynamic> env) async {
    if (state != VoiceState.accepting) return;
    final extra = env['extra'] as Map?;
    if (extra?['call_id']?.toString() != _callId) return;
    _pc = await _createPC();
    _localStream!.getTracks().forEach((t) => _pc!.addTrack(t, _localStream!));
    await _pc!.setRemoteDescription(RTCSessionDescription(extra!['sdp'].toString(), 'offer'));
    final answer = await _pc!.createAnswer();
    await _pc!.setLocalDescription(answer);
    sendEnvelope({
      'type': 'voice_answer',
      'to': _callerFrom,
      'ts': DateTime.now().millisecondsSinceEpoch,
      'extra': {'call_id': _callId, 'sdp': answer.sdp},
    });
    state = VoiceState.talking;
    _startTs = DateTime.now();
    _resetTimer(const Duration(seconds: 1), () {}, repeat: true, onTick: () {
      if (state != VoiceState.talking) return;
      final sec = DateTime.now().difference(_startTs!).inSeconds;
      final mm = (sec ~/ 60).toString().padLeft(2, '0');
      final ss = (sec % 60).toString().padLeft(2, '0');
      statusText = '通话中 $mm:$ss';
      notifyListeners();
    });
    notifyListeners();
  }

  Future<void> _onIce(Map<String, dynamic> env) async {
    if (_pc == null) return;
    final extra = env['extra'] as Map?;
    if (extra == null) return;
    try {
      await _pc!.addCandidate(RTCIceCandidate(
        extra['candidate']?.toString(),
        extra['sdpMid']?.toString(),
        (extra['sdpMLineIndex'] is int)
            ? extra['sdpMLineIndex']
            : int.tryParse(extra['sdpMLineIndex']?.toString() ?? '0'),
      ));
    } catch (_) {}
  }

  void _onRemoteEnd(Map<String, dynamic> env) {
    if (state == VoiceState.idle) return;
    _end('对方已挂断', notify: false);
  }

  /// 用户主动挂断
  void hangup() {
    if (state == VoiceState.idle) return;
    _end('您挂断了', notify: true);
  }

  /// 切换免提（扬声器）/ 听筒。仅 talking 状态生效。
  /// iOS/Android 都靠 flutter_webrtc 的 Helper.setSpeakerphoneOn 切音频路由。
  Future<void> toggleSpeaker() async {
    if (state != VoiceState.talking) return;
    final next = !speakerOn;
    try {
      await Helper.setSpeakerphoneOn(next);
      speakerOn = next;
      notifyListeners();
    } catch (_) {
      // 部分设备不支持，静默
    }
  }

  void _end(String reason, {required bool notify}) {
    if (state == VoiceState.idle) return;
    if (notify && _callerFrom != null && _callId != null) {
      // [034] 与三端对齐：把 reason 映射成后端识别的 code + duration（秒）。
      // 后端 hub→service.OnVoiceCallFinished 会据此写一条 sys 消息进聊天记录。
      String code = 'hangup';
      int duration = 0;
      if (state == VoiceState.incoming || state == VoiceState.accepting) {
        code = 'cancel';
      } else if (state == VoiceState.talking && _startTs != null) {
        code = 'hangup';
        duration = DateTime.now().difference(_startTs!).inSeconds;
      }
      if (reason.startsWith('连接中断') || reason.startsWith('麦克风失败')) {
        code = 'failed';
      }
      sendEnvelope({
        'type': 'voice_end',
        'to': _callerFrom,
        'ts': DateTime.now().millisecondsSinceEpoch,
        'extra': {'call_id': _callId, 'code': code, 'duration': duration},
      });
    }
    _cleanup();
    state = VoiceState.ended;
    statusText = reason;
    notifyListeners();
    Timer(const Duration(milliseconds: 2500), () {
      if (state == VoiceState.ended) {
        state = VoiceState.idle;
        notifyListeners();
      }
    });
  }

  void _cleanup() {
    _pc?.close();
    _pc = null;
    _localStream?.getTracks().forEach((t) => t.stop());
    _localStream = null;
    _remoteStream = null;
    _stopTimer();
    _callId = null;
    _callerFrom = null;
    // 复位扬声器状态，让下次通话重新从听筒（私密模式）开始
    if (speakerOn) {
      try { Helper.setSpeakerphoneOn(false); } catch (_) {}
      speakerOn = false;
    }
  }

  void _resetTimer(Duration d, void Function() onDone, {bool repeat = false, void Function()? onTick}) {
    _stopTimer();
    if (repeat) {
      _timer = Timer.periodic(d, (_) => (onTick ?? onDone)());
    } else {
      _timer = Timer(d, onDone);
    }
  }

  void _stopTimer() {
    _timer?.cancel();
    _timer = null;
  }

  @override
  void dispose() {
    _cleanup();
    super.dispose();
  }
}
