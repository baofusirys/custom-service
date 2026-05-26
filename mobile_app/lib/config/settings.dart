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
