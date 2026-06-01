import 'dart:convert';
import 'package:shared_preferences/shared_preferences.dart';

/// 应用持久化配置（后端 URL、登录 token、agent 资料）。
///
/// 设计：单实例 ChangeNotifier 外面套；这里只暴露 async getter/setter。
/// 切换服务器时会自动清掉 token + agent（强制重新登录）。
class Settings {
  static const _keyBackendUrl = 'backend_url';
  static const _keyAgentToken = 'agent_token';
  static const _keyAgentJson = 'agent_json';

  static Future<SharedPreferences> get _sp => SharedPreferences.getInstance();

  // ===== backend URL =====
  static Future<String?> getBackendUrl() async {
    return (await _sp).getString(_keyBackendUrl);
  }

  static Future<void> setBackendUrl(String url) async {
    final sp = await _sp;
    // 规范化：去掉末尾 / 和空白
    final normalized = url.trim().replaceAll(RegExp(r'/+$'), '');
    final old = sp.getString(_keyBackendUrl);
    await sp.setString(_keyBackendUrl, normalized);
    // 切换到新服务器 → 自动登出（旧 token 跟原服务器走，新服务器无效）
    if (old != null && old != normalized) {
      await sp.remove(_keyAgentToken);
      await sp.remove(_keyAgentJson);
    }
  }

  static Future<void> clearBackendUrl() async {
    final sp = await _sp;
    await sp.remove(_keyBackendUrl);
    await sp.remove(_keyAgentToken);
    await sp.remove(_keyAgentJson);
  }

  // ===== Agent session =====
  static Future<String?> getAgentToken() async {
    return (await _sp).getString(_keyAgentToken);
  }

  static Future<Map<String, dynamic>?> getAgent() async {
    final raw = (await _sp).getString(_keyAgentJson);
    if (raw == null) return null;
    try {
      return jsonDecode(raw) as Map<String, dynamic>;
    } catch (_) {
      return null;
    }
  }

  static Future<void> setSession(String token, Map<String, dynamic> agent) async {
    final sp = await _sp;
    await sp.setString(_keyAgentToken, token);
    await sp.setString(_keyAgentJson, jsonEncode(agent));
  }

  /// [064] 仅更新 token，不动 agent json。
  /// /agent/login/refresh 接口只返新 token 不返 agent 资料（agent 资料没变），
  /// 调用方拿到新 token 后直接 setAgentToken 就行。
  static Future<void> setAgentToken(String token) async {
    final sp = await _sp;
    await sp.setString(_keyAgentToken, token);
  }

  static Future<void> clearSession() async {
    final sp = await _sp;
    await sp.remove(_keyAgentToken);
    await sp.remove(_keyAgentJson);
  }

  // ===== [070] 消息本地缓存（进会话秒显，冷启动也不丢）=====
  // 每会话一个 key 'msgs:<convId>' 存最近 N 条消息 JSON；另用 'msgs:_index'
  // 维护 convId 的 LRU 顺序，超过 _maxCachedConvs 个会话就淘汰最旧的，防止
  // shared_preferences 无限膨胀。客服场景会话/消息量可控，够用且零额外依赖。
  static const _msgCachePrefix = 'msgs:';
  static const _msgIndexKey = 'msgs:_index';
  static const _maxCachedConvs = 60;
  static const _maxMsgsPerConv = 200;

  static Future<List<Map<String, dynamic>>> getCachedMessages(String convId) async {
    final raw = (await _sp).getString('$_msgCachePrefix$convId');
    if (raw == null) return const [];
    try {
      final v = jsonDecode(raw);
      if (v is List) return v.cast<Map<String, dynamic>>();
    } catch (_) {}
    return const [];
  }

  static Future<void> setCachedMessages(String convId, List<Map<String, dynamic>> msgs) async {
    final sp = await _sp;
    final trimmed = msgs.length > _maxMsgsPerConv
        ? msgs.sublist(msgs.length - _maxMsgsPerConv)
        : msgs;
    await sp.setString('$_msgCachePrefix$convId', jsonEncode(trimmed));
    // 更新 LRU index + 淘汰最旧会话缓存
    List<String> idx = const [];
    final idxRaw = sp.getString(_msgIndexKey);
    if (idxRaw != null) {
      try { idx = (jsonDecode(idxRaw) as List).cast<String>(); } catch (_) {}
    }
    final next = <String>[convId, ...idx.where((c) => c != convId)];
    while (next.length > _maxCachedConvs) {
      final evict = next.removeLast();
      await sp.remove('$_msgCachePrefix$evict');
    }
    await sp.setString(_msgIndexKey, jsonEncode(next));
  }

  static Future<void> clearMessageCache() async {
    final sp = await _sp;
    final idxRaw = sp.getString(_msgIndexKey);
    if (idxRaw != null) {
      try {
        for (final c in (jsonDecode(idxRaw) as List).cast<String>()) {
          await sp.remove('$_msgCachePrefix$c');
        }
      } catch (_) {}
    }
    await sp.remove(_msgIndexKey);
  }

  /// 把 http://host:port → ws://host:port 转换（用于 WSS endpoint）
  static String httpToWs(String backendUrl) {
    if (backendUrl.startsWith('https://')) {
      return 'wss://' + backendUrl.substring(8);
    }
    if (backendUrl.startsWith('http://')) {
      return 'ws://' + backendUrl.substring(7);
    }
    return backendUrl;
  }
}
