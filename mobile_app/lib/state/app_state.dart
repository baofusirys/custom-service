import 'dart:async';
import 'dart:io';
import 'package:flutter/foundation.dart';
import '../api/http_client.dart';
import '../api/models.dart';
import '../api/sound.dart' as snd;
import '../api/ws_client.dart';
import '../config/settings.dart';
import 'voice_controller.dart';

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

  // [066] 「全部 / 已联系」过滤模式（同步 [065] admin Console 行为）。
  //   'all'       : 所有进行中会话
  //   'contacted' : 仅 hasVisitorMsg=true 或 unread>0 的会话（c.isContacted）
  // 用 ValueNotifier 让 SegmentedButton 可以单独监听切换、避免整页 rebuild；
  // 同时 AppState 改变时也会触发 ConversationsPage 的 Consumer 重渲染。
  final ValueNotifier<String> filterMode = ValueNotifier<String>('all');

  /// [066] 按 filterMode 过滤后的会话列表（UI 列表的真实数据源）。
  List<Conversation> get filteredConvs {
    if (filterMode.value == 'contacted') {
      return convs.where((c) => c.isContacted).toList(growable: false);
    }
    return convs;
  }

  /// [066] 「已联系」计数（用于 SegmentedButton 上的 `已联系 (M)` 标签）。
  int get contactedCount => convs.where((c) => c.isContacted).length;

  /// [066] 切换 filter 并通知 UI；放在 AppState 上保持与 openConv 等行为一致。
  void setFilterMode(String mode) {
    if (mode != 'all' && mode != 'contacted') return;
    if (filterMode.value == mode) return;
    filterMode.value = mode;
    notifyListeners();
  }

  // ===== WSS =====
  AgentWS? _ws;
  bool get wsAlive => _ws?.isAlive ?? false;
  // 自己 WSS 连接 ID（从 hello envelope 拿）—— 多端同步去重的关键
  String? myConnId;
  // 客服端通知音色（admin 才能拉到完整设置，普通客服 fallback 默认）
  String agentSound = 'agent1';

  // ===== Voice 通话控制器（全局单例，UI 用 ListenableBuilder 监听） =====
  final VoiceController voice = VoiceController();

  Future<void> loadAgentSound() async {
    if (agent?.role != 'admin') return;
    try {
      final s = await Api.getSettings();
      agentSound = (s['agent_notify_sound'] ?? 'agent1').toString();
    } catch (_) {}
  }

  // [064] 监听 Api.authFailedStream：token 完全失效（grace > 24h / agent 禁用）时
  // 自动 logout 让顶层 _Root 跳到 LoginPage。
  StreamSubscription<void>? _authFailedSub;

  Future<void> bootstrap() async {
    backendUrl = await Settings.getBackendUrl();
    token = await Settings.getAgentToken();
    final a = await Settings.getAgent();
    if (a != null) agent = Agent.fromJson(a);
    // [064] 订阅 401 失效广播（http_client.dart Dio interceptor 触发）
    _authFailedSub ??= Api.authFailedStream.listen((_) async {
      // 已经在 logout 路径里了就跳过
      if (token == null) return;
      await logout();
    });
    notifyListeners();
  }

  @override
  void dispose() {
    _authFailedSub?.cancel();
    _authFailedSub = null;
    filterMode.dispose();
    super.dispose();
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

  /// [051] 加载状态标志：openConv 立刻返、ChatPage 据此显示 spinner
  /// true = 后台 HTTP 拉消息中；false = 拉完（或失败/无消息）
  bool loadingMessages = false;

  /// [051] 即时切换，不阻塞 UI。
  /// 改前：await listMessages + await assign 后才返回 → ConversationsPage 等 1-2s 才 push 页面
  /// 改后：立刻设 activeConv + notifyListeners → 调用方立刻 push 页面（0ms 感知切换），
  /// 后台 unawaited 跑 HTTP，拉到消息后再 notifyListeners 让 ChatPage 重渲染
  void openConv(Conversation c) {
    activeConv = c;
    messages.clear();
    loadingMessages = true;
    notifyListeners();
    // 防 race：用户快速切换会话时，旧的 HTTP 回来不能覆盖新的 activeConv
    final reqConvId = c.id;
    () async {
      try {
        final raw = await Api.listMessages(reqConvId, limit: 100);
        // 用户已切到别的会话 / 关闭了，丢弃本次结果
        if (activeConv?.id != reqConvId) return;
        final list = raw.map(Message.fromJson).toList()
          ..sort((a, b) => a.createdAt.compareTo(b.createdAt));
        messages
          ..clear()
          ..addAll(list);
        await Api.assign(reqConvId);
        if (activeConv?.id != reqConvId) return;
        c.unread = 0;
        for (final m in messages) {
          if (m.sender == 'visitor') m.read = true;
        }
        _sendRead(reqConvId);
      } catch (_) {
        // 静默：网络出错 ChatPage 显示空 + 用户可下拉重试（未来加）
      } finally {
        if (activeConv?.id == reqConvId) {
          loadingMessages = false;
          notifyListeners();
        }
      }
    }();
  }

  void closeActive() {
    activeConv = null;
    messages.clear();
    loadingMessages = false;  // [051] 离开会话清 loading 标志，下次打开重新置 true
    notifyListeners();
  }

  // [068] 防 race 设计说明（mobile 端）：
  //   mobile_app 现状已天然不串台 —— ChatPage 是 push 新页面方式打开，
  //   每个 conv 对应一个独立 _ChatPageState 实例，其 _input (TextEditingController)
  //   随 ChatPage 生命周期独立创建/销毁（见 chat_page.dart line 19 / line 38-43），
  //   切换会话走 pop+push 必然换新实例，输入框文本 0 串台。
  //   但仍按「snapshot-on-entry」防御范式（与 admin Console [068] 对齐）：
  //   在 sendChat / uploadAndSendFile 入口立即锁定 convId snapshot，
  //   后续 ws.send 的 conv 字段、Message.convId、本地乐观渲染守卫全部使用 snapshot，
  //   防止未来重构在函数体中多次读 activeConv.id 时被中途切走的 race 触发数据错配。
  Future<void> sendChat(String text) async {
    final conv = activeConv;
    if (conv == null || text.trim().isEmpty || _ws == null || agent == null) return;
    final convIdSnap = conv.id;              // [068] 入口锁定 conv，杜绝中途切走
    final textSnap = text.trim();            // [068] 入口锁定文本
    final now = DateTime.now();
    _ws!.send({
      'type': 'chat',
      'conv': convIdSnap,
      'content': textSnap,
      'ts': now.millisecondsSinceEpoch,
      'prio': 0,
    });
    // [068] 仅当用户仍停在 snapshot 会话才本地乐观渲染，
    //   切走了消息已正确发到原 conv，只是 UI 不污染新会话视图。
    if (activeConv?.id == convIdSnap) {
      messages.add(Message(
        id: 'local-${now.millisecondsSinceEpoch}',
        convId: convIdSnap,
        sender: 'agent',
        senderRef: agent!.id.toString(),
        content: textSnap,
        createdAt: now,
      ));
      notifyListeners();
    }
  }

  /// [041] 客服上传文件并发出 media 消息（图片 / 文件，跟 admin web Console.vue 对齐）。
  /// 失败返回 false，调用方可用来 toast / 重试。
  /// [068] 入口 snapshot convIdSnap，上传 / ws.send / 乐观渲染全部锁定原 conv，
  ///   防止上传期间 (await Api.uploadFile) 用户切走导致文件挂到错误会话。
  Future<bool> uploadAndSendFile(File file) async {
    final conv = activeConv;
    if (conv == null || _ws == null || agent == null) return false;
    final convIdSnap = conv.id;              // [068] 入口锁定 conv
    final res = await Api.uploadFile(file, convIdSnap);
    if (res == null) return false;
    final url = res['url']?.toString() ?? '';
    if (url.isEmpty) return false;
    final kind = res['kind']?.toString() ?? '';
    final name = res['name']?.toString() ?? '';
    final size = (res['size'] is int) ? res['size'] as int : 0;
    final now = DateTime.now();
    _ws!.send({
      'type': 'chat',
      'conv': convIdSnap,
      'content': '',
      'media': url,
      'mkind': kind,
      'mname': name,
      'msize': size,
      'ts': now.millisecondsSinceEpoch,
      'prio': 0,
    });
    // [068] 切走了就不污染新会话 UI，但文件已正确发到 convIdSnap
    if (activeConv?.id == convIdSnap) {
      messages.add(Message(
        id: 'local-${now.millisecondsSinceEpoch}',
        convId: convIdSnap,
        sender: 'agent',
        senderRef: agent!.id.toString(),
        content: '',
        mediaUrl: url,
        mediaKind: kind,
        mediaName: name,
        createdAt: now,
      ));
      notifyListeners();
    }
    return true;
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
    // 注入信令发送 + agent 身份到 VoiceController
    voice.sendEnvelope = (m) => _ws?.send(m);
    voice.agentId = agent?.id.toString() ?? '';
    voice.agentNickname = agent?.nickname ?? '';
  }

  void stopWs() {
    _ws?.stop();
    _ws = null;
    myConnId = null;
  }

  void _onEnvelope(Map<String, dynamic> env) {
    final type = env['type']?.toString();
    if (type == 'pong') return;
    // 语音通话信令统一分发到 VoiceController（独立状态机，不污染 AppState）
    if (type != null && type.startsWith('voice_')) {
      voice.handleSignal(env);
      return;
    }
    if (type == 'hello') {
      // 记住自己 connID（多端去重必需）
      final extra = env['extra'];
      if (extra is Map && extra['conn_id'] is String) {
        myConnId = extra['conn_id'] as String;
      }
      return;
    }
    if (type == 'sys') {
      // 访客进入通知 -> 播声
      final extra = env['extra'];
      if (extra is Map && extra['kind']?.toString() == 'visitor_enter') {
        snd.playSound(agentSound);
      }
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

    // [067] 口径收紧：voice 通话事件不再算「已主动联系」。
    //   - 访客文字 (fromVisitor)              → hasVisitorMsg=true（+ unread++ 仍走原逻辑）
    //   - 访客 voice 通话事件 (fromSys + kind='voice') → 仅刷新预览/排序，不动 hasVisitorMsg、不动 unread
    //   - 其余 sys（page_navigation 等）        → 不动 hasVisitorMsg、不动 unread
    // 与 admin Console + 后端 SQL EXISTS 严格对齐：只有 messages.sender='visitor' 才翻牌。

    if (activeConv != null && convId == activeConv!.id) {
      messages.add(m);
      // 同步更新当前会话的 lastMessage 预览
      activeConv!.lastMessageSender = senderTag;
      activeConv!.lastMessagePreview = preview;
      activeConv!.updatedAt = m.createdAt;
      if (fromVisitor) {
        activeConv!.hasVisitorMsg = true;
        _sendRead(convId);
        snd.playSound(agentSound);
      }
      notifyListeners();
      return;
    }

    // 非当前会话：本地 unread++ + 上浮（不走 HTTP，0 延迟）
    final idx = convs.indexWhere((x) => x.id == convId);
    if (idx >= 0) {
      final c = convs[idx];
      if (fromVisitor) {
        c.unread++;
        c.hasVisitorMsg = true;
        snd.playSound(agentSound);
      }
      // [067] voice sys 不再翻 hasVisitorMsg；只走下面的 updatedAt / 预览刷新 + 上浮
      c.updatedAt = m.createdAt;
      c.lastMessageSender = senderTag;
      c.lastMessagePreview = preview;
      if (idx > 0) {
        convs.removeAt(idx);
        convs.insert(0, c);
      }
      notifyListeners();
    } else if (fromVisitor || fromSys) {
      // 列表里还没有这个 conv → 拉接口（后端会带 has_visitor_msg=true 给前端）
      refreshConvs();
      if (fromVisitor) snd.playSound(agentSound);
    }
  }
}
