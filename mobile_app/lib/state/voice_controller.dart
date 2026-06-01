import 'dart:async';
import 'package:flutter/foundation.dart';
import 'package:flutter_webrtc/flutter_webrtc.dart';
import '../api/http_client.dart';
import '../api/sound.dart' show playRingLoop, stopRingLoop;

// ============================================================================
// [069] 语音通话「17 秒卡死」修复 —— mobile_app 侧 Patch 1 + 2 + 4
// ----------------------------------------------------------------------------
// 起因：iOS / Android 客服端接听后偶尔卡 17 秒才显示「通话失败」，UI 无具体原因。
// 根因：
//   1) _onOffer 三个 await（setRemoteDescription / createAnswer / setLocalDescription）
//      串在一起没有 try/catch，任何一个抛错都变成 unhandled async exception，
//      state 卡在 accepting，UI 既不切 talking 也不切 ended，直到 visitor 端 17s
//      超时挂断才被动结束。
//   2) accept() 里 getUserMedia 失败只本地 _end，**不通知 backend / visitor**，
//      visitor 端必须等 30s offer 超时。
//   3) 推送唤醒场景：voice_offer 比 PC 创建还早到，_onOffer 直接 return（state 不
//      对），visitor 永远等不到 voice_answer。
//
// 本次修复（mobile 侧）：
//   [Patch 1] _onOffer 三阶段独立 try/catch + 详细 debugPrint(phase + stack)
//             + sendEnvelope voice_signal_error {phase, reason, call_id} 让 backend
//             立刻 fanout voice_finished 给 visitor，5 秒内告知失败 + 原因。
//   [Patch 2] accept() 顶部 getUserMedia preflight，失败立刻 sendEnvelope
//             voice_accept_failed {reason: mic_permission_denied / mic_busy / ...}，
//             backend 收到后 < 50ms 广播 voice_finished，把 17s 卡死缩到 < 1s。
//   [Patch 4] _prepareForIncomingCall：voice_call 一到达就 createPeerConnection
//             + 挂 onIceCandidate / onTrack / onConnectionState，配合 _earlyIceQueue
//             缓存早到的 ICE candidate，消除推送唤醒下的冷启动 race。
//
// 防御性硬化（次要）：
//   - 所有 await _pc!.xxx() 都包 try/catch，避免一处异常击穿整个状态机
//   - _onIce 不再 silent drop：_pc==null 时 candidate 入队 _earlyIceQueue，
//     等 _onOffer 内 setRemoteDescription 完成后 flush
//
// 配套后端：backend/internal/ws/hub.go 新增 voice_signal_error / voice_accept_failed
//          case + voice_accept 5s 看门狗 + voice_answer 取消看门狗。
// ============================================================================

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
  // ICE 配置默认仅 Google STUN 兜底；accept() 时会调 Api.turnCredential() 刷新加上 TURN，
  // 让通话在严格 NAT / VPN 下也能走中继 ([035])
  Map<String, dynamic> _iceServers = const {
    'iceServers': [
      {'urls': 'stun:stun.l.google.com:19302'}
    ]
  };

  // 拉短期 TURN 凭证（24h TTL）。失败保持默认 STUN，调用方不阻塞。
  Future<void> _refreshIceServers() async {
    final cred = await Api.turnCredential();
    if (cred == null) return;
    final urls = cred['urls'];
    if (urls is! List || urls.isEmpty) return;
    final srv = <String, dynamic>{'urls': urls};
    if (cred['username'] != null) srv['username'] = cred['username'];
    if (cred['credential'] != null) srv['credential'] = cred['credential'];
    _iceServers = {'iceServers': [srv]};
  }

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

  // [069-P4] _pcReady：标记 _pc 已 createPeerConnection + 挂好 callbacks
  // _prepareForIncomingCall() 成功置 true；_cleanup 复位 false。
  // 用于幂等（重复 _prepareForIncomingCall 直接跳过）+ _onOffer 兜底判断。
  bool _pcReady = false;

  // [069-P4] _earlyIceQueue：voice_offer 还没到 / setRemoteDescription 还没完成
  // 时收到的 ICE candidate 暂存队列，setRemoteDescription 完成后统一 flush。
  // 推送唤醒场景下 visitor 端会 trickle ICE 比 offer 早到，旧逻辑会 silent drop。
  final List<RTCIceCandidate> _earlyIceQueue = [];

  // 信令分发入口（由 AppState._onEnvelope 调）
  void handleSignal(Map<String, dynamic> env) {
    final type = env['type']?.toString() ?? '';
    switch (type) {
      case 'voice_call':     _onIncoming(env); break;
      case 'voice_taken':    _onTaken(env); break;
      case 'voice_offer':    _onOffer(env); break;
      case 'voice_ice':      _onIce(env); break;
      case 'voice_end':      _onRemoteEnd(env); break;
      // [069-P1/P3] backend 5s 看门狗 / signal_error 路径会下发 voice_finished
      // 立刻关浮窗 + 给用户看到具体 reason（不是「通话失败」黑盒）
      case 'voice_finished': _onRemoteFinished(env); break;
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
    // [036] 来电铃声循环；accept / reject / _end / _cleanup 任何路径都会停
    // App 在前台：直接触发；App 在后台：APNs 推送拉起 App 后 buffer 重投 voice_call 再触发
    playRingLoop();
    _resetTimer(const Duration(seconds: 30), () {
      if (state == VoiceState.incoming) _end('未接听', notify: false);
    });
    notifyListeners();

    // [069-P4] voice_call 一到达就预热 PeerConnection：
    //   - 推送唤醒场景（visitor 端 voice_accept 后立刻 trickle ICE）下，
    //     accept() 还没跑完 visitor 的 candidate 就已经到了，旧逻辑会 silent drop。
    //   - 预热后 _onIce 把早到的 candidate 入 _earlyIceQueue，
    //     _onOffer setRemoteDescription 成功后统一 flush 进 _pc。
    //   - 失败不阻塞主流程，accept() 内会兜底再创一次。
    _prepareForIncomingCall().catchError((e, st) {
      debugPrint('[voice][_onIncoming] _prepareForIncomingCall failed (will retry in accept): $e\n$st');
    });
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
    if (state != VoiceState.incoming) {
      debugPrint('[voice][accept] dropped: state=$state (expect incoming)');
      return;
    }
    _stopTimer();
    stopRingLoop();  // [036] 接听立刻停铃声（accept 路径不进 _cleanup）
    debugPrint('[voice][accept] begin callId=$_callId callerFrom=$_callerFrom');
    // 接听前刷一次 TURN 凭证，让 createPeerConnection 用最新 iceServers（含 TURN）
    // 失败不阻塞，fallback 到默认 STUN
    try {
      await _refreshIceServers();
      debugPrint('[voice][accept] iceServers refreshed');
    } catch (e, st) {
      debugPrint('[voice][accept] _refreshIceServers warning (non-fatal): $e\n$st');
    }

    // [069-P2] 麦克风 preflight（CRITICAL）：必须在发 voice_accept 之前完成。
    //   失败立刻 sendEnvelope voice_accept_failed 通知 backend，让 visitor 端
    //   < 1s 内收到「麦克风权限被拒 / 麦克风被占用 / 硬件异常」具体原因，
    //   取代旧路径走到 _onOffer 才挂 + visitor 等 17s 超时的体验。
    try {
      debugPrint('[voice][accept] phase=getUserMedia begin');
      _localStream = await navigator.mediaDevices.getUserMedia({
        'audio': true,
        'video': false,
      });
      final tracks = _localStream?.getAudioTracks() ?? const [];
      if (tracks.isEmpty) {
        throw StateError('no_audio_tracks');
      }
      debugPrint('[voice][accept] phase=getUserMedia ok, tracks=${tracks.length}');
    } catch (e, st) {
      final reason = _classifyMicError(e);
      debugPrint('[voice][accept] phase=getUserMedia FAILED reason=$reason err=$e\n$st');
      // 上报 backend：新协议 voice_accept_failed
      // backend hub.go [069-P2] case 收到后立刻 fanout voice_finished + 写 sys 消息
      try {
        sendEnvelope({
          'type': 'voice_accept_failed',
          'to': _callerFrom,
          'ts': DateTime.now().millisecondsSinceEpoch,
          'extra': {
            'call_id': _callId,
            'reason': reason,        // mic_permission_denied / mic_busy / mic_hardware_error / no_audio_tracks / mic_unknown
            'detail': e.toString(),
            'agent_id': agentId,
          },
        });
      } catch (sendErr) {
        debugPrint('[voice][accept] failed to send voice_accept_failed: $sendErr');
      }
      _end('麦克风失败：$reason', notify: false);
      return;
    }

    // [069-P4] PC 预热兜底：_prepareForIncomingCall 通常已在 _onIncoming 阶段跑过；
    // 冷启动 / 推送唤醒 race 下若未跑完，这里再创一次。幂等。
    try {
      if (!_pcReady || _pc == null) {
        await _prepareForIncomingCall();
      }
      // _localStream 拿到后才能 addTrack（_prepareForIncomingCall 仅创建 PC、不挂 track）
      for (final t in _localStream!.getAudioTracks()) {
        try {
          await _pc!.addTrack(t, _localStream!);
        } catch (e, st) {
          debugPrint('[voice][accept] addTrack failed (non-fatal, continue): $e\n$st');
        }
      }
      debugPrint('[voice][accept] PC ready + addTrack ok');
    } catch (e, st) {
      debugPrint('[voice][accept] phase=pc_prepare FAILED: $e\n$st');
      _sendSignalError(phase: 'pc_prepare_after_mic', reason: e.toString());
      _end('PC 准备失败', notify: true);
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
    debugPrint('[voice][accept] voice_accept + voice_taken sent');
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

  /// [069-P4] 预热 PeerConnection。
  ///
  /// 调用时机：
  ///   1. _onIncoming 末尾（voice_call 一到就预热，不等用户点接听）
  ///   2. accept() 兜底（_pc 仍为空时再创一次）
  ///
  /// 行为：
  ///   - createPeerConnection + 注册 onIceCandidate / onTrack / onConnectionState
  ///     / onIceConnectionState 回调
  ///   - **不** addTrack（_localStream 此时还没 getUserMedia，accept 阶段才挂）
  ///   - **不** setRemoteDescription（offer 还没到，_onOffer 内统一做）
  ///   - 成功设 _pcReady=true；失败保持 _pc=null，accept() 会兜底重试
  ///
  /// 幂等：多次调用安全（_pc != null 直接 return）
  Future<void> _prepareForIncomingCall() async {
    if (_pcReady && _pc != null) {
      debugPrint('[voice][_prepareForIncomingCall] already ready, skip');
      return;
    }
    debugPrint('[voice][_prepareForIncomingCall] begin');
    try {
      final pc = await createPeerConnection(_iceServers);

      // ICE candidate 回调：peerconnection 发现的本地 candidate trickle 给 visitor
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

      // 远端音轨：flutter_webrtc 自动路由到扬声器，无需挂 audio widget
      pc.onTrack = (event) {
        if (event.streams.isNotEmpty) {
          _remoteStream = event.streams.first;
          debugPrint('[voice][onTrack] remote stream attached');
        }
      };

      pc.onConnectionState = (s) {
        debugPrint('[voice][onConnectionState] $s');
        if (s == RTCPeerConnectionState.RTCPeerConnectionStateFailed ||
            s == RTCPeerConnectionState.RTCPeerConnectionStateDisconnected) {
          // [069-P1] 同步上报给 backend 让 visitor 也立刻收到 voice_finished
          _sendSignalError(phase: 'ice', reason: 'ice_disconnected');
          _end('连接中断', notify: true);
        }
      };

      pc.onIceConnectionState = (s) {
        debugPrint('[voice][onIceConnectionState] $s');
        if (s == RTCIceConnectionState.RTCIceConnectionStateFailed) {
          // [069-P1] 仅上报，不立刻 _end（让 ConnectionState 的 failed/disconnected 决定）
          _sendSignalError(phase: 'ice', reason: 'no_ice_candidate');
        }
      };

      _pc = pc;
      _pcReady = true;
      debugPrint('[voice][_prepareForIncomingCall] PC ready (no tracks yet)');
    } catch (e, st) {
      debugPrint('[voice][_prepareForIncomingCall] FAILED: $e\n$st');
      _pc = null;
      _pcReady = false;
      // 不上报 voice_signal_error（accept 还没发 voice_accept，无 call 在途）
      rethrow;
    }
  }

  Future<void> _onOffer(Map<String, dynamic> env) async {
    // [069-P1] guard 放宽：accepting 是主路径，incoming 是 race（offer 比 accept 早送达）
    if (state != VoiceState.accepting && state != VoiceState.incoming) {
      debugPrint('[voice][_onOffer] dropped: state=$state (expect accepting)');
      return;
    }
    final extra = env['extra'] as Map?;
    final cid = extra?['call_id']?.toString();
    if (cid != _callId) {
      debugPrint('[voice][_onOffer] dropped: callId mismatch env=$cid local=$_callId');
      return;
    }
    final sdp = extra?['sdp']?.toString();
    if (sdp == null || sdp.isEmpty) {
      debugPrint('[voice][_onOffer] FATAL: empty sdp in offer');
      _sendSignalError(phase: 'parse_offer', reason: 'empty_sdp');
      _end('信令异常：空 SDP', notify: true);
      return;
    }

    // [069-P4] PC 兜底创建（_prepareForIncomingCall 通常已跑过）
    try {
      if (_pc == null) {
        debugPrint('[voice][_onOffer] PC not preheated, create now');
        await _prepareForIncomingCall();
        // 此时 _localStream 可能已在 accept() 拿到，补挂 track
        if (_localStream != null) {
          for (final t in _localStream!.getTracks()) {
            try {
              await _pc!.addTrack(t, _localStream!);
            } catch (e, st) {
              debugPrint('[voice][_onOffer] addTrack fallback failed (non-fatal): $e\n$st');
            }
          }
        }
      }
    } catch (e, st) {
      debugPrint('[voice][_onOffer] phase=create_pc FAILED: $e\n$st');
      _sendSignalError(phase: 'create_pc', reason: e.toString());
      _end('信令异常：PC 创建失败', notify: true);
      return;
    }

    // [069-P1] 阶段 1：setRemoteDescription（offer）
    try {
      debugPrint('[voice][_onOffer] phase=setRemoteDescription begin (sdp ${sdp.length}B)');
      await _pc!.setRemoteDescription(RTCSessionDescription(sdp, 'offer'));
      debugPrint('[voice][_onOffer] phase=setRemoteDescription ok');
      // [069-P4] flush 早到的 ICE candidate（在 setRemoteDescription 之前是没法 addCandidate 的）
      if (_earlyIceQueue.isNotEmpty) {
        debugPrint('[voice][_onOffer] flush earlyIceQueue size=${_earlyIceQueue.length}');
        for (final cand in _earlyIceQueue) {
          try {
            await _pc!.addCandidate(cand);
          } catch (e, st) {
            debugPrint('[voice][_onOffer] flush ICE failed (non-fatal): $e\n$st');
          }
        }
        _earlyIceQueue.clear();
      }
    } catch (e, st) {
      debugPrint('[voice][_onOffer] phase=setRemoteDescription FAILED: $e\n$st');
      _sendSignalError(phase: 'setRemoteDescription', reason: e.toString());
      _end('信令异常：SDP 解析失败', notify: true);
      return;
    }

    // [069-P1] 阶段 2：createAnswer
    RTCSessionDescription? answer;
    try {
      debugPrint('[voice][_onOffer] phase=createAnswer begin');
      answer = await _pc!.createAnswer();
      debugPrint('[voice][_onOffer] phase=createAnswer ok (sdp ${answer.sdp?.length ?? 0}B)');
    } catch (e, st) {
      debugPrint('[voice][_onOffer] phase=createAnswer FAILED: $e\n$st');
      _sendSignalError(phase: 'createAnswer', reason: e.toString());
      _end('信令异常：应答生成失败', notify: true);
      return;
    }

    // [069-P1] 阶段 3：setLocalDescription
    try {
      debugPrint('[voice][_onOffer] phase=setLocalDescription begin');
      await _pc!.setLocalDescription(answer);
      debugPrint('[voice][_onOffer] phase=setLocalDescription ok');
    } catch (e, st) {
      debugPrint('[voice][_onOffer] phase=setLocalDescription FAILED: $e\n$st');
      _sendSignalError(phase: 'setLocalDescription', reason: e.toString());
      _end('信令异常：本地描述设置失败', notify: true);
      return;
    }

    // 全部成功：发 voice_answer + 切 talking
    sendEnvelope({
      'type': 'voice_answer',
      'to': _callerFrom,
      'ts': DateTime.now().millisecondsSinceEpoch,
      'extra': {'call_id': _callId, 'sdp': answer.sdp},
    });
    debugPrint('[voice][_onOffer] voice_answer sent, state -> talking');
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
    final extra = env['extra'] as Map?;
    if (extra == null) return;
    final candidate = RTCIceCandidate(
      extra['candidate']?.toString(),
      extra['sdpMid']?.toString(),
      (extra['sdpMLineIndex'] is int)
          ? extra['sdpMLineIndex']
          : int.tryParse(extra['sdpMLineIndex']?.toString() ?? '0'),
    );
    // [069-P4] _pc 还没创建好：入队，等 _onOffer setRemoteDescription 完成后 flush
    // 旧逻辑直接 return 会丢 candidate，是推送唤醒下无法建立 ICE 的根因之一
    if (_pc == null) {
      _earlyIceQueue.add(candidate);
      debugPrint('[voice][_onIce] PC not ready, queued (size=${_earlyIceQueue.length})');
      return;
    }
    try {
      await _pc!.addCandidate(candidate);
    } catch (e, st) {
      // 防御性 try/catch：addCandidate 失败不击穿状态机（通常是无效 candidate，可忽略）
      debugPrint('[voice][_onIce] addCandidate failed (non-fatal): $e\n$st');
    }
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
    stopRingLoop();  // [036] 统一停铃声：reject / _end / dispose 都进这里
    try {
      _pc?.close();
    } catch (e, st) {
      // [069] 防御性 try/catch：close 抛错不影响后续清理
      debugPrint('[voice][_cleanup] pc.close failed (non-fatal): $e\n$st');
    }
    _pc = null;
    _pcReady = false;          // [069-P4] 复位预热标记
    _earlyIceQueue.clear();    // [069-P4] 清空残留 candidate
    _localStream?.getTracks().forEach((t) {
      try { t.stop(); } catch (_) {}
    });
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

  // ===========================================================================
  // [069] 辅助方法：信令错误上报 / 麦克风错误分类 / 远端 finished 处理
  // ===========================================================================

  /// [069-P1] 上报信令异常给 backend。
  ///
  /// 协议：type=voice_signal_error，backend hub.go 收到后写 bizLog +
  /// fanout voice_finished(reason=signal_exception, phase=<phase>) 给 visitor，
  /// 让对方也立刻关浮窗显示具体原因，并落 sys 消息进聊天记录。
  ///
  /// phase 取值：setRemoteDescription / createAnswer / setLocalDescription
  ///           / create_pc / mic_preflight / pc_prepare_after_mic
  ///           / parse_offer / ice
  void _sendSignalError({required String phase, required String reason}) {
    try {
      sendEnvelope({
        'type': 'voice_signal_error',
        'to': _callerFrom,
        'ts': DateTime.now().millisecondsSinceEpoch,
        'extra': {
          'call_id': _callId,
          'phase': phase,
          'reason': reason,
          'agent_id': agentId,
        },
      });
      debugPrint('[voice] reported voice_signal_error phase=$phase reason=$reason');
    } catch (e, st) {
      debugPrint('[voice] _sendSignalError itself failed: $e\n$st');
    }
  }

  /// [069-P2] 把 getUserMedia 抛出的异常归一化成 backend 能识别的 reason enum。
  ///
  /// flutter_webrtc 在 iOS / Android / Web 的异常字符串不一致，0.9.x 版本里
  /// 主要靠 toString() 做模糊匹配。覆盖：
  ///   - iOS：PlatformException(code=permission_denied / NotAllowedError)
  ///   - Android：SecurityException / CameraAccessException(in_use)
  ///   - Web：NotAllowedError / NotReadableError / OverconstrainedError
  String _classifyMicError(Object e) {
    final s = e.toString().toLowerCase();
    if (s.contains('permission') || s.contains('notallowederror') || s.contains('denied')) {
      return 'mic_permission_denied';
    }
    if (s.contains('busy') || s.contains('inuse') || s.contains('in_use') || s.contains('notreadable')) {
      return 'mic_busy';
    }
    if (s.contains('overconstrained') || s.contains('notfound') || s.contains('hardware')) {
      return 'mic_hardware_error';
    }
    if (s.contains('no_audio_tracks')) {
      return 'no_audio_tracks';
    }
    return 'mic_unknown';
  }

  /// [069-P1/P3] 收到 backend 5s 看门狗 / signal_error 路径下发的 voice_finished：
  /// 立刻关浮窗 + 显示具体 reason（不是「通话失败」黑盒）。
  /// backend 已写好 sys 消息，此处不再回发 voice_end 避免重复落库。
  void _onRemoteFinished(Map<String, dynamic> env) {
    if (state == VoiceState.idle) return;
    final extra = env['extra'] as Map?;
    final cid = extra?['call_id']?.toString();
    if (cid != null && _callId != null && cid != _callId) {
      debugPrint('[voice][_onRemoteFinished] callId mismatch env=$cid local=$_callId');
      return;
    }
    final reason = extra?['reason']?.toString() ?? '';
    final code = extra?['code']?.toString() ?? '';
    debugPrint('[voice][_onRemoteFinished] code=$code reason=$reason');
    _end(_reasonToText(code, reason), notify: false);
  }

  /// [069] 把 backend 下发的 code/reason 翻译成中文 UI 文案。
  /// 跟 backend service.go codeToText 对齐（避免双端不一致）。
  String _reasonToText(String code, String reason) {
    switch (reason) {
      case 'agent_no_answer_5s':    return '5 秒未应答，系统自动挂断';
      case 'mic_permission_denied': return '麦克风权限被拒，无法接听';
      case 'mic_busy':              return '麦克风被其他程序占用';
      case 'mic_hardware_error':    return '麦克风硬件异常';
      case 'no_audio_tracks':       return '未能获取音频通道';
      case 'signal_exception':      return '信令异常，通话已中止';
      case 'no_answer_sdp':         return '应答 SDP 解析失败';
      case 'no_ice_candidate':      return 'ICE 候选超时';
      case 'ice_disconnected':      return '网络中断';
      default:
        if (code == 'no_answer') return '对方未接听';
        if (code == 'failed') return '通话失败';
        if (code == 'rejected') return '对方已拒绝';
        if (code == 'busy') return '对方忙线中';
        if (code == 'cancel') return '已取消';
        return '通话已结束';
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
