import 'dart:io';
import 'dart:async';
import 'package:dio/dio.dart';
import '../config/settings.dart';

/// 全局 HTTP 客户端。每次 baseUrl 变化都重建。
///
/// [064] 加 401 自动 refresh 拦截器解决 [068] iOS App 12h token 过期死循环：
///   - onError 拦截 401，如果是 token 过期（code=40102）→ 调 /agent/login/refresh
///   - refreshTokenLock 互斥锁：多个并发 401 只触发 1 次 refresh
///   - refresh 成功 → 自动用新 token 重试原请求
///   - refresh 失败（grace > 24h 或 agent 禁用）→ 触发 authFailedStream，
///     上层 main.dart 监听后跳登录页
class Api {
  static Dio? _dio;
  static String? _baseUrl;
  static String? _token;

  // [064] refresh 互斥锁：多个 API 同时 401 → 只调 1 次 /agent/login/refresh
  static Completer<bool>? _refreshLock;

  // [064] token 完全失效（grace > 24h 或 agent 禁用）→ 广播通知 main.dart 跳登录页
  static final StreamController<void> _authFailedController =
      StreamController<void>.broadcast();
  static Stream<void> get authFailedStream => _authFailedController.stream;

  static Future<Dio> _ensure() async {
    final url = await Settings.getBackendUrl();
    final token = await Settings.getAgentToken();
    if (_dio == null || _baseUrl != url || _token != token) {
      _baseUrl = url;
      _token = token;
      _dio = Dio(BaseOptions(
        baseUrl: (url ?? '') + '/api',
        connectTimeout: const Duration(seconds: 10),
        receiveTimeout: const Duration(seconds: 15),
        headers: {
          'Content-Type': 'application/json',
          if (token != null && token.isNotEmpty) 'Authorization': 'Bearer $token',
        },
      ));

      // [064] Dio 401 拦截器：自动 refresh + retry
      _dio!.interceptors.add(InterceptorsWrapper(
        onError: (DioException err, handler) async {
          final res = err.response;
          if (res?.statusCode != 401) {
            return handler.next(err);
          }
          // 区分错误码：
          //   40102 token expired → 走 refresh 流程
          //   其他 (40101 未登录 / 40103 token 无效) → 直接走 auth failed
          final code = (res?.data is Map) ? res!.data['code'] : null;
          if (code != 40102) {
            await _onAuthFailed();
            return handler.next(err);
          }
          // 试图 refresh
          final ok = await _refreshToken();
          if (!ok) {
            // refresh 也失败：grace > 24h 或 agent 禁用 → 跳登录
            await _onAuthFailed();
            return handler.next(err);
          }
          // refresh 成功 → 用新 token 重试原请求一次
          try {
            final newToken = await Settings.getAgentToken();
            final opts = err.requestOptions;
            opts.headers['Authorization'] = 'Bearer $newToken';
            // 注意：用 fetch 而非 dio.request，避免触发 baseUrl 拼接（opts 里已是完整 path）
            final retry = await _dio!.fetch(opts);
            return handler.resolve(retry);
          } catch (_) {
            // 重试失败也走 authFailed
            await _onAuthFailed();
            return handler.next(err);
          }
        },
      ));
    }
    return _dio!;
  }

  /// [064] 用现有 token 换新 token。
  /// 并发安全：_refreshLock 互斥锁防多个 401 同时触发；同一时刻只 1 次 refresh。
  /// 返回 true=成功（新 token 已 setAgentToken），false=失败（应跳登录页）。
  static Future<bool> _refreshToken() async {
    // 已经在 refresh 中：等待并复用结果
    if (_refreshLock != null) {
      return _refreshLock!.future;
    }
    final lock = Completer<bool>();
    _refreshLock = lock;
    try {
      final oldToken = await Settings.getAgentToken();
      if (oldToken == null || oldToken.isEmpty) {
        lock.complete(false);
        return false;
      }
      // 单独的 Dio 不带 interceptor，避免无限递归（refresh 失败再触发 401 拦截再 refresh…）
      final tmpDio = Dio(BaseOptions(
        baseUrl: (_baseUrl ?? '') + '/api',
        connectTimeout: const Duration(seconds: 10),
        receiveTimeout: const Duration(seconds: 10),
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer $oldToken',
        },
      ));
      Response r;
      try {
        r = await tmpDio.post('/agent/login/refresh');
      } catch (_) {
        lock.complete(false);
        return false;
      }
      if (r.statusCode == 200 && r.data is Map && r.data['code'] == 0) {
        final newToken = r.data['token'] as String?;
        if (newToken != null && newToken.isNotEmpty) {
          await Settings.setAgentToken(newToken);
          // 强制下次 _ensure() 重建 Dio 拿新 token header
          invalidate();
          lock.complete(true);
          return true;
        }
      }
      lock.complete(false);
      return false;
    } catch (_) {
      lock.complete(false);
      return false;
    } finally {
      // 100ms 后释放锁，给等待中的 caller 一点时间拿到结果再清
      Future.delayed(const Duration(milliseconds: 100), () {
        if (_refreshLock == lock) _refreshLock = null;
      });
    }
  }

  /// [064] 公开入口给 ws_client.dart 主动 refresh 用（WSS 连接前检查 exp）
  static Future<bool> refreshTokenPublic() async {
    return _refreshToken();
  }

  /// token 完全失效 → 清 session + 广播让 main.dart 跳登录页
  static Future<void> _onAuthFailed() async {
    await Settings.clearSession();
    invalidate();
    _authFailedController.add(null);
  }

  /// 当 token 或 url 变化时主动清掉单例。
  static void invalidate() {
    _dio = null;
    _baseUrl = null;
    _token = null;
  }

  // ===== Auth =====
  static Future<Map<String, dynamic>> login(String username, String password) async {
    final dio = await _ensure();
    final r = await dio.post('/agent/login', data: {'username': username, 'password': password});
    return Map<String, dynamic>.from(r.data);
  }

  static Future<Map<String, dynamic>> health() async {
    final dio = await _ensure();
    final r = await dio.get('/health');
    return Map<String, dynamic>.from(r.data);
  }

  /// 拉 WebRTC TURN/STUN 凭证（24h 短期）。
  /// 失败返回 null —— 调用方应 fallback 到默认 STUN 让通话至少能尝试 P2P。
  /// 详见 backend/internal/service/turn.go + turn/README.md（[035]）
  static Future<Map<String, dynamic>?> turnCredential() async {
    try {
      final dio = await _ensure();
      final r = await dio.get('/agent/turn-credential');
      if (r.data is Map && r.data['code'] == 0 && r.data['data'] is Map) {
        return Map<String, dynamic>.from(r.data['data']);
      }
    } catch (_) { /* 静默：调用方做 fallback */ }
    return null;
  }

  /// 用临时 baseUrl 测 health，不污染全局 _dio——服务器配置页用，避免提前
  /// notifyListeners 导致页面 dispose 后 setState 报错。
  static Future<Map<String, dynamic>> healthAt(String baseUrl) async {
    final tmpDio = Dio(BaseOptions(
      baseUrl: baseUrl + '/api',
      connectTimeout: const Duration(seconds: 10),
      receiveTimeout: const Duration(seconds: 15),
      headers: {'Content-Type': 'application/json'},
    ));
    final r = await tmpDio.get('/health');
    return Map<String, dynamic>.from(r.data);
  }

  // ===== Upload [041] =====
  /// 客服上传图片 / 文件。POST /api/upload multipart：file + uploader=agent + conv_id
  /// 后端校验：authorizeUpload 用 Bearer JWT + 校验 conv_id 是当前 agent 能接管的会话
  /// 后端返回：{id, url, kind, size, name, mime}（无 code 包装）
  /// 失败返回 null，调用方静默或自己提示
  static Future<Map<String, dynamic>?> uploadFile(File file, String convId) async {
    try {
      final dio = await _ensure();
      // 文件名兼容 Windows 路径分隔符（Mac/iOS 是 /，但 Path 类一致更稳）
      final filename = file.path.split(Platform.pathSeparator).last;
      final fd = FormData.fromMap({
        'file': await MultipartFile.fromFile(file.path, filename: filename),
        'uploader': 'agent',
        'conv_id': convId,
      });
      // 显式去掉 application/json 默认头，让 dio 自动按 multipart 拼 Content-Type + boundary
      final r = await dio.post(
        '/upload',
        data: fd,
        options: Options(headers: {'Content-Type': 'multipart/form-data'}),
      );
      if (r.data is Map) return Map<String, dynamic>.from(r.data);
    } catch (_) { /* 静默 */ }
    return null;
  }

  // ===== Conversations =====
  static Future<List<Map<String, dynamic>>> listConversations() async {
    final dio = await _ensure();
    final r = await dio.get('/agent/conversations');
    final data = r.data['data'];
    if (data is List) return data.cast<Map<String, dynamic>>();
    return const [];
  }

  /// [070] 加 after 增量同步参数：传 after=<本地最后一条消息 id> 只拉比它更新的消息，
  /// 用于进会话先显本地缓存、再后台补这段增量（before 仍用于向上翻历史）。
  static Future<List<Map<String, dynamic>>> listMessages(String convId, {int limit = 100, String? before, String? after}) async {
    final dio = await _ensure();
    final r = await dio.get('/agent/conversations/$convId/messages',
        queryParameters: {
          'limit': limit,
          if (before != null) 'before': before,
          if (after != null) 'after': after,
        });
    final data = r.data['data'];
    if (data is List) return data.cast<Map<String, dynamic>>();
    return const [];
  }

  static Future<void> assign(String convId) async {
    final dio = await _ensure();
    await dio.post('/agent/conversations/$convId/assign');
  }

  static Future<void> markRead(String convId) async {
    final dio = await _ensure();
    await dio.post('/agent/conversations/$convId/read');
  }

  static Future<void> closeConv(String convId) async {
    final dio = await _ensure();
    await dio.post('/agent/conversations/$convId/close');
  }

  // ===== Settings (admin only) =====
  static Future<Map<String, dynamic>> getSettings() async {
    final dio = await _ensure();
    final r = await dio.get('/admin/settings');
    return Map<String, dynamic>.from(r.data['data'] ?? {});
  }

  static Future<void> saveSettings(Map<String, String> payload) async {
    final dio = await _ensure();
    await dio.post('/admin/settings', data: payload);
  }

  // ===== Agents (admin only) =====
  static Future<List<Map<String, dynamic>>> listAgents() async {
    final dio = await _ensure();
    final r = await dio.get('/admin/agents');
    final data = r.data['data'];
    if (data is List) return data.cast<Map<String, dynamic>>();
    return const [];
  }

  static Future<int> createAgent({
    required String username,
    required String password,
    required String role,
    required String nickname,
  }) async {
    final dio = await _ensure();
    final r = await dio.post('/admin/agents', data: {
      'username': username,
      'password': password,
      'role': role,
      'nickname': nickname,
    });
    final id = r.data['id'];
    return id is int ? id : int.tryParse(id.toString()) ?? 0;
  }

  static Future<void> setAgentActive(int id, bool active) async {
    final dio = await _ensure();
    await dio.post('/admin/agents/active', data: {'id': id, 'active': active});
  }
}
