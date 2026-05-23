import 'package:flutter/foundation.dart';
import '../api/http_client.dart';
import '../api/models.dart';
import '../api/ws_client.dart';
import '../config/settings.dart';

/// 全局应用状态。负责：
///   - 维护当前 agent / token / backendUrl
///   - 维护会话列表 + 当前打开的会话消息
///   - 管理唯一 WSS 连接（agent 长连）
///
/// 用 ChangeNotifier + Provider。轻量、零魔法。
class AppState extends ChangeNotifier {
  // ===== Session =====
  String? backendUrl;
  String? token;
  Agent? agent;

  // ===== Conversation list =====
  final List<Conversation> convs = [];
  Conversation? activeConv;
  final List<Message> messages = [];

  // ===== WSS =====
  AgentWS? _ws;
  bool get wsAlive => _ws?.isAlive ?? false;

  Future<void> bootstrap() async {
    backendUrl = await Settings.getBackendUrl();
    token = await Settings.getAgentToken();
    final a = await Settings.getAgent();
    if (a != null) agent = Agent.fromJson(a);
    notifyListeners();
  }

  // ===== Login / Logout =====
  Future<String?> login(String username, String password) async {
    try {
      final r = await Api.login(username, password);
      if (r['code'] != 0) {
        return r['msg']?.toString() ?? '登录失败';
      }
      final tk = r['token']?.toString() ?? '';
      final ag = Agent.fromJson(Map<String, dynamic>.from(r['agent'] ?? {}));
      await Settings.setSession(tk, ag.toJson());
      Api.invalidate();
      token = tk;
      agent = ag;
      notifyListeners();
      return null;
    } catch (e) {
      return '网络错误：$e';
    }
  }

  Future<void> logout() async {
    stopWs();
    convs.clear();
    activeConv = null;
    messages.clear();
    await Settings.clearSession();
    Api.invalidate();
    token = null;
    agent = null;
    notifyListeners();
  }

  Future<void> setBackend(String url) async {
    await Settings.setBackendUrl(url);
    backendUrl = url;
    // 切换服务器 → 清掉旧 session
    stopWs();
    convs.clear();
    activeConv = null;
    messages.clear();
    token = null;
    agent = null;
    Api.invalidate();
    notifyListeners();
  }

  // ===== Conversations =====
  Future<void> refreshConvs() async {
    try {
      final raw = await Api.listConversations();
      convs
        ..clear()
        ..addAll(raw.map(Conversation.fromJson));
      notifyListeners();
    } catch (_) {}
  }

  Future<void> openConv(Conversation c) async {
    activeConv = c;
    messages.clear();
    notifyListeners();
    try {
      final raw = await Api.listMessages(c.id, limit: 100);
      final list = raw.map(Message.fromJson).toList()
        ..sort((a, b) => a.createdAt.compareTo(b.createdAt));
      messages
        ..clear()
        ..addAll(list);
      await Api.assign(c.id);
      c.unread = 0;
      // 标记本地所有访客消息已读 + 推 WSS read
      for (final m in messages) {
        if (m.sender == 'visitor') m.read = true;
      }
      _sendRead(c.id);
      notifyListeners();
    } catch (_) {}
  }

  void closeActive() {
    activeConv = null;
    messages.clear();
    notifyListeners();
  }

  Future<void> sendChat(String text) async {
    final conv = activeConv;
    if (conv == null || text.trim().isEmpty || _ws == null || agent == null) return;
    final now = DateTime.now();
    _ws!.send({
      'type': 'chat',
      'conv': conv.id,
      'content': text.trim(),
      'ts': now.millisecondsSinceEpoch,
      'prio': 0,
    });
    // 乐观渲染
    messages.add(Message(
      id: 'local-${now.millisecondsSinceEpoch}',
      convId: conv.id,
      sender: 'agent',
      senderRef: agent!.id.toString(),
      content: text.trim(),
      createdAt: now,
    ));
    notifyListeners();
  }

  void _sendRead(String convId) {
    _ws?.send({
      'type': 'read',
      'conv': convId,
      'ts': DateTime.now().millisecondsSinceEpoch,
    });
  }

  // ===== WSS =====
  void startWs() {
    if (_ws != null || backendUrl == null || token == null) return;
    final wsBase = Settings.httpToWs(backendUrl!);
    _ws = AgentWS(
      wsBaseUrl: wsBase,
      token: token!,
      onOpen: () => notifyListeners(),
      onClose: () => notifyListeners(),
      onEnvelope: _onEnvelope,
    );
    _ws!.start();
  }

  void stopWs() {
    _ws?.stop();
    _ws = null;
  }

  void _onEnvelope(Map<String, dynamic> env) {
    final type = env['type']?.toString();
    if (type == 'pong' || type == 'hello') return;
    if (type == 'sys') {
      // 访客进入通知 / 其他系统消息
      refreshConvs();
      return;
    }
    if (type == 'read') {
      if (activeConv == null || env['conv']?.toString() != activeConv!.id) return;
      final from = env['from']?.toString() ?? '';
      if (from.startsWith('visitor:')) {
        final ts = env['ts'] is int ? env['ts'] : 0;
        final upTo = DateTime.fromMillisecondsSinceEpoch(ts);
        for (final m in messages) {
          if (m.sender == 'agent' &&
              m.senderRef == agent?.id.toString() &&
              !m.read &&
              m.createdAt.compareTo(upTo) <= 0) {
            m.read = true;
          }
        }
        notifyListeners();
      }
      return;
    }
    if (type != 'chat') return;

    final from = env['from']?.toString() ?? '';
    final fromAgent = from.startsWith('agent:');
    final fromVisitor = from.startsWith('visitor:');
    final fromSys = from == 'sys';
    final isMyOwn = fromAgent && from.split(':').last == agent?.id.toString();
    if (isMyOwn) return;

    final convId = env['conv']?.toString() ?? '';
    final extra = env['extra'] is Map ? Map<String, dynamic>.from(env['extra']) : null;
    final kind = extra?['kind']?.toString();

    final isPageNav = fromSys && kind == 'page_navigation';
    final senderRef = isPageNav
        ? 'page:' + (extra?['url']?.toString() ?? '')
        : (from.contains(':') ? from.split(':').last : (fromSys ? 'system' : ''));

    final m = Message(
      id: env['id']?.toString() ?? '',
      convId: convId,
      sender: fromAgent ? 'agent' : (fromSys ? 'sys' : 'visitor'),
      senderRef: senderRef,
      content: env['content']?.toString() ?? '',
      mediaUrl: env['media']?.toString() ?? '',
      mediaKind: env['mkind']?.toString() ?? '',
      mediaName: env['mname']?.toString() ?? '',
      createdAt: env['ts'] is int
          ? DateTime.fromMillisecondsSinceEpoch(env['ts'])
          : DateTime.now(),
      pageUrl: isPageNav ? (extra?['url']?.toString() ?? '') : '',
      pageTitle: isPageNav ? (extra?['title']?.toString() ?? '') : '',
    );

    if (activeConv != null && convId == activeConv!.id) {
      messages.add(m);
      if (fromVisitor) _sendRead(convId);
      notifyListeners();
      return;
    }

    // 非当前会话：本地 unread++ + 上浮（不走 HTTP，0 延迟）
    final idx = convs.indexWhere((x) => x.id == convId);
    if (idx >= 0) {
      final c = convs[idx];
      if (fromVisitor) c.unread++;
      c.updatedAt = m.createdAt;
      if (idx > 0) {
        convs.removeAt(idx);
        convs.insert(0, c);
      }
      notifyListeners();
    } else if (fromVisitor || fromSys) {
      refreshConvs();
    }
  }
}
