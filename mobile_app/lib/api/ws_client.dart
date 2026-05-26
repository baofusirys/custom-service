import 'dart:async';
import 'dart:convert';
import 'package:web_socket_channel/web_socket_channel.dart';
import 'http_client.dart';
import '../config/settings.dart';

/// 客服端 WSS 客户端：
///   - 自动重连（指数退避，最高 30s）
///   - 30s 心跳 ping
///   - 暴露 onEnvelope 回调让上层 AppState 处理消息
///
/// [064] 解决 [068] iOS App 12h 后 401 死循环：
///   - token 改可变（不再 final），refresh 后能更新
///   - _isConnecting 重入锁
///   - 连接前主动检查 exp，距过期 < 5min 主动 refresh
///   - 区分 close code：4001 token expired / 4002 token invalid（虽然实际 WSS handshake 401
///     时 Flutter 拿不到自定义 code，所以仍然靠主动检查 exp 兜底）
///   - refresh 失败时 stop 不再死循环重连
class AgentWS {
  final String wsBaseUrl; // 例：ws://38.76.193.68 （不含 /ws/agent）
  String _token; // [064] 可变：refresh 后更新
  final void Function(Map<String, dynamic> env) onEnvelope;
  final void Function()? onOpen;
  final void Function()? onClose;

  WebSocketChannel? _ch;
  bool _alive = false;
  bool _shouldRun = false;
  bool _isConnecting = false; // [064] 重入锁
  int _retry = 0;
  Timer? _heartbeat;
  Timer? _reconnect;

  AgentWS({
    required this.wsBaseUrl,
    required String token,
    required this.onEnvelope,
    this.onOpen,
    this.onClose,
  }) : _token = token;

  String get token => _token;
  bool get isAlive => _alive;

  void start() {
    _shouldRun = true;
    _connect();
  }

  void stop() {
    _shouldRun = false;
    _heartbeat?.cancel();
    _reconnect?.cancel();
    _isConnecting = false;
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

  void _connect() async {
    // [064] 重入锁：防多个并发 _connect 同时跑
    if (_isConnecting) return;
    _isConnecting = true;

    try {
      // [064] 连接前主动检查 token 是否快过期（距 exp < 5min）→ 先 refresh
      // 这是解决 [068] 的关键路径：WSS handshake 401 时 Flutter 拿不到自定义 close code，
      // 所以必须客户端自己提前判断。
      if (_shouldRefreshToken()) {
        final ok = await Api.refreshTokenPublic();
        if (ok) {
          final newToken = await Settings.getAgentToken();
          if (newToken != null && newToken.isNotEmpty) {
            _token = newToken;
          }
        } else {
          // refresh 失败 → 不再重连，等 Api authFailedStream 触发 main.dart 跳登录页
          _isConnecting = false;
          stop();
          return;
        }
      }

      final url = '$wsBaseUrl/ws/agent?token=${Uri.encodeQueryComponent(_token)}';
      try {
        _ch = WebSocketChannel.connect(Uri.parse(url));
      } catch (_) {
        _isConnecting = false;
        if (_shouldRun) _scheduleReconnect();
        return;
      }
      _alive = true;
      _retry = 0;
      _isConnecting = false;
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
        onDone: () => _onClose(closeCode: _ch?.closeCode),
        onError: (_) => _onClose(closeCode: null),
        cancelOnError: true,
      );
    } catch (_) {
      _isConnecting = false;
      if (_shouldRun) _scheduleReconnect();
    }
  }

  void _onClose({int? closeCode}) {
    _alive = false;
    _heartbeat?.cancel();
    onClose?.call();
    // [064] 区分 close code：
    //   4001 = token expired → 主动 refresh + 重连
    //   4002 = token invalid → 完全失效不重连
    // 注：服务端目前 reject 在 WSS upgrade 之前发 HTTP 401，Flutter 拿到的 closeCode
    // 通常是 null 而非 4001/4002。所以这里更多是「未来增强」，主路径靠
    // _shouldRefreshToken() 在 _connect 时主动检查。
    if (closeCode == 4001) {
      _refreshAndReconnect();
      return;
    }
    if (closeCode == 4002) {
      stop();
      return;
    }
    if (_shouldRun) _scheduleReconnect();
  }

  Future<void> _refreshAndReconnect() async {
    final ok = await Api.refreshTokenPublic();
    if (ok) {
      final newToken = await Settings.getAgentToken();
      if (newToken != null && newToken.isNotEmpty) {
        _token = newToken;
        _retry = 0;
        if (_shouldRun) _connect();
        return;
      }
    }
    // refresh 失败：不再死循环重连
    stop();
  }

  /// [064] 检测 token 距离过期 < 5 分钟。
  /// 解析 JWT payload（base64-url 解码）拿 exp 字段跟当前时间比较。
  /// 解析失败 / 没有 exp → 返 false（继续用现有 token，让服务端决定）。
  bool _shouldRefreshToken() {
    try {
      final parts = _token.split('.');
      if (parts.length != 3) return false;
      var payload = parts[1];
      // base64-url → base64 标准 padding
      payload = payload.replaceAll('-', '+').replaceAll('_', '/');
      payload = payload.padRight(payload.length + (4 - payload.length % 4) % 4, '=');
      final decoded = utf8.decode(base64.decode(payload));
      final json = jsonDecode(decoded);
      if (json is Map && json['exp'] is int) {
        final expMs = (json['exp'] as int) * 1000;
        final nowMs = DateTime.now().millisecondsSinceEpoch;
        return (expMs - nowMs) < 5 * 60 * 1000; // 距过期 < 5 分钟
      }
    } catch (_) {}
    return false;
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
