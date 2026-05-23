import 'dart:async';
import 'dart:convert';
import 'package:web_socket_channel/web_socket_channel.dart';

/// 客服端 WSS 客户端：
///   - 自动重连（指数退避，最高 30s）
///   - 30s 心跳 ping
///   - 暴露 onEnvelope 回调让上层 AppState 处理消息
class AgentWS {
  final String wsBaseUrl; // 例：ws://38.76.193.68 （不含 /ws/agent）
  final String token;
  final void Function(Map<String, dynamic> env) onEnvelope;
  final void Function()? onOpen;
  final void Function()? onClose;

  WebSocketChannel? _ch;
  bool _alive = false;
  bool _shouldRun = false;
  int _retry = 0;
  Timer? _heartbeat;
  Timer? _reconnect;

  AgentWS({
    required this.wsBaseUrl,
    required this.token,
    required this.onEnvelope,
    this.onOpen,
    this.onClose,
  });

  bool get isAlive => _alive;

  void start() {
    _shouldRun = true;
    _connect();
  }

  void stop() {
    _shouldRun = false;
    _heartbeat?.cancel();
    _reconnect?.cancel();
    try {
      _ch?.sink.close();
    } catch (_) {}
    _ch = null;
    _alive = false;
  }

  /// 主动发送 envelope（type=chat / read / typing 等）
  bool send(Map<String, dynamic> env) {
    if (!_alive || _ch == null) return false;
    try {
      _ch!.sink.add(jsonEncode(env));
      return true;
    } catch (_) {
      return false;
    }
  }

  void _connect() {
    final url = '$wsBaseUrl/ws/agent?token=${Uri.encodeQueryComponent(token)}';
    try {
      _ch = WebSocketChannel.connect(Uri.parse(url));
    } catch (_) {
      _scheduleReconnect();
      return;
    }
    _alive = true;
    _retry = 0;
    onOpen?.call();
    _heartbeat = Timer.periodic(const Duration(seconds: 30), (_) {
      if (_alive) {
        try {
          _ch!.sink.add(jsonEncode({'type': 'ping', 'ts': DateTime.now().millisecondsSinceEpoch}));
        } catch (_) {}
      }
    });
    _ch!.stream.listen(
      (data) {
        if (data is String) {
          try {
            final env = jsonDecode(data);
            if (env is Map<String, dynamic>) onEnvelope(env);
          } catch (_) {}
        }
      },
      onDone: () {
        _alive = false;
        _heartbeat?.cancel();
        onClose?.call();
        if (_shouldRun) _scheduleReconnect();
      },
      onError: (_) {
        _alive = false;
        _heartbeat?.cancel();
        onClose?.call();
        if (_shouldRun) _scheduleReconnect();
      },
      cancelOnError: true,
    );
  }

  void _scheduleReconnect() {
    final backoffMs = (1000 * (1.6 * (_retry + 1)).round()).clamp(1000, 30000);
    _retry++;
    _reconnect?.cancel();
    _reconnect = Timer(Duration(milliseconds: backoffMs), () {
      if (_shouldRun) _connect();
    });
  }
}
