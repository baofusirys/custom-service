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
  // 自己 WSS 连接 ID（从 hello envelope 拿）—— 多端同步去重的关键
  String? myConnId;

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
    myConnId = null;
  }

  void _onEnvelope(Map<String, dynamic> env) {
    final type = env['type']?.toString();
    if (type == 'pong') return;
    if (type == 'hello') {
      // 记住自己 connID（多端去重必需）
      final extra = env['extra'];
      if (extra is Map && extra['conn_id'] is String) {
        myConnId = extra['conn_id'] as String;
      }
      return;
    }
    if (type == 'sys') {
      refreshConvs();
      return;
    }

    final myId = agent?.id.toString() ?? '';
    final convId = env['conv']?.toString() ?? '';

    if (type == 'read') {
      final from = env['from']?.toString() ?? '';
      final fromAgent = from.startsWith('agent:');
      final isFromMyAccount = fromAgent && from.split(':').last == myId;
      // 同账号在另一端（web/app）读了 → 同步清掉本端该 conv 的 unread
      if (isFromMyAccount && env['conn']?.toString() != myConnId) {
        final idx = convs.indexWhere((c) => c.id == convId);
        if (idx >= 0 && convs[idx].unread > 0) {
          convs[idx].unread = 0;
          notifyListeners();
        }
        return;
      }
      // 访客读了客服消息 → 当前会话标 mine 已读
      if (activeConv == null || convId != activeConv!.id) return;
      if (from.startsWith('visitor:')) {
        final ts = env['ts'] is int ? env['ts'] : 0;
        final upTo = DateTime.fromMillisecondsSinceEpoch(ts);
        for (final m in messages) {
          if (m.sender == 'agent' &&
              m.senderRef == myId &&
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
    // 多端去重：只有自己这一端的 connID 才算"回声"跳过；
    // 同账号其他端发的消息正常接受（实现双端同步）
    final isMyConnEcho = env['conn']?.toString() == myConnId && myConnId != null && myConnId!.isNotEmpty;
    if (isMyConnEcho) return;

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

    // 计算预览文本（图片/文件占位）
    String preview = m.content;
    if (preview.isEmpty && m.mediaKind == 'image') preview = '[图片]';
    else if (preview.isEmpty && m.mediaUrl.isNotEmpty) preview = '[文件]';
    final senderTag = fromAgent ? 'agent' : (fromSys ? 'sys' : 'visitor');

    if (activeConv != null && convId == activeConv!.id) {
      messages.add(m);
      // 同步更新当前会话的 lastMessage 预览
      activeConv!.lastMessageSender = senderTag;
      activeConv!.lastMessagePreview = preview;
      activeConv!.updatedAt = m.createdAt;
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
      c.lastMessageSender = senderTag;
      c.lastMessagePreview = preview;
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
