import 'package:dio/dio.dart';
import '../config/settings.dart';

/// 全局 HTTP 客户端。每次 baseUrl 变化都重建。
class Api {
  static Dio? _dio;
  static String? _baseUrl;
  static String? _token;

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
    }
    return _dio!;
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

  // ===== Conversations =====
  static Future<List<Map<String, dynamic>>> listConversations() async {
    final dio = await _ensure();
    final r = await dio.get('/agent/conversations');
    final data = r.data['data'];
    if (data is List) return data.cast<Map<String, dynamic>>();
    return const [];
  }

  static Future<List<Map<String, dynamic>>> listMessages(String convId, {int limit = 100, String? before}) async {
    final dio = await _ensure();
    final r = await dio.get('/agent/conversations/$convId/messages',
        queryParameters: {'limit': limit, if (before != null) 'before': before});
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
