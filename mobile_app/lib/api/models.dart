/// 数据模型 —— 与 backend/internal/store/store.go 的 JSON 字段保持一致。

class Agent {
  final int id;
  final String username;
  final String role;
  final String nickname;

  Agent({required this.id, required this.username, required this.role, required this.nickname});

  factory Agent.fromJson(Map<String, dynamic> j) => Agent(
        id: j['id'] is int ? j['id'] : int.tryParse(j['id'].toString()) ?? 0,
        username: j['username']?.toString() ?? '',
        role: j['role']?.toString() ?? 'agent',
        nickname: j['nickname']?.toString() ?? '',
      );

  Map<String, dynamic> toJson() => {
        'id': id,
        'username': username,
        'role': role,
        'nickname': nickname,
      };
}

class Conversation {
  final String id;
  final String visitorId;
  int unread;
  DateTime startedAt;
  DateTime updatedAt;
  final String identifier;
  final String country;
  final String city;
  final String lastPage;
  final String referer;
  // 最后一条消息预览（拉列表时填，WSS 实时也会维护）
  String lastMessageSender;  // 'agent' / 'visitor' / 'sys' / ''
  String lastMessagePreview; // 文本预览（图片/文件已替换为 [图片]/[文件]）
  // [066] 同步后端 [065]：访客是否真正发过消息或拨打过 voice_call。
  //   true  = 访客主动联系过（文字消息 or voice 通话 sys 事件）
  //   false = 仅浏览 / 仅 page_navigation 等系统事件
  // 后端 ListOpenConversations EXISTS 子查询给出，WSS chat fromVisitor 时本地也会置 true。
  bool hasVisitorMsg;

  Conversation({
    required this.id,
    required this.visitorId,
    this.unread = 0,
    required this.startedAt,
    required this.updatedAt,
    this.identifier = '',
    this.country = '',
    this.city = '',
    this.lastPage = '',
    this.referer = '',
    this.lastMessageSender = '',
    this.lastMessagePreview = '',
    this.hasVisitorMsg = false,
  });

  factory Conversation.fromJson(Map<String, dynamic> j) {
    String lmSender = '';
    String lmContent = '';
    final lm = j['last_message'];
    if (lm is Map) {
      lmSender = lm['sender']?.toString() ?? '';
      lmContent = lm['content']?.toString() ?? '';
    }
    return Conversation(
      id: j['id']?.toString() ?? '',
      visitorId: j['visitor_id']?.toString() ?? '',
      unread: j['unread'] is int ? j['unread'] : int.tryParse(j['unread'].toString()) ?? 0,
      startedAt: _parseDate(j['started_at']),
      updatedAt: _parseDate(j['updated_at']),
      identifier: j['identifier']?.toString() ?? '',
      country: j['country']?.toString() ?? '',
      city: j['city']?.toString() ?? '',
      lastPage: j['last_page']?.toString() ?? '',
      referer: j['referer']?.toString() ?? '',
      lastMessageSender: lmSender,
      lastMessagePreview: lmContent,
      // [066] snake_case → camelCase；后端旧版无此字段时默认 false（fallback 兜底见 isContacted）
      hasVisitorMsg: j['has_visitor_msg'] == true,
    );
  }

  /// [066] 可选输出（当前 mobile_app 无本地缓存场景，预留给未来持久化）。
  Map<String, dynamic> toJson() => {
        'id': id,
        'visitor_id': visitorId,
        'unread': unread,
        'started_at': startedAt.toIso8601String(),
        'updated_at': updatedAt.toIso8601String(),
        'identifier': identifier,
        'country': country,
        'city': city,
        'last_page': lastPage,
        'referer': referer,
        'has_visitor_msg': hasVisitorMsg,
      };

  /// [066] 复制并覆盖部分字段。注意 unread / hasVisitorMsg 是可变字段，
  /// copyWith 仅在需要"快照式"克隆时使用；运行中通常直接改对象本身（与现有 openConv 等保持一致）。
  Conversation copyWith({
    String? id,
    String? visitorId,
    int? unread,
    DateTime? startedAt,
    DateTime? updatedAt,
    String? identifier,
    String? country,
    String? city,
    String? lastPage,
    String? referer,
    String? lastMessageSender,
    String? lastMessagePreview,
    bool? hasVisitorMsg,
  }) {
    return Conversation(
      id: id ?? this.id,
      visitorId: visitorId ?? this.visitorId,
      unread: unread ?? this.unread,
      startedAt: startedAt ?? this.startedAt,
      updatedAt: updatedAt ?? this.updatedAt,
      identifier: identifier ?? this.identifier,
      country: country ?? this.country,
      city: city ?? this.city,
      lastPage: lastPage ?? this.lastPage,
      referer: referer ?? this.referer,
      lastMessageSender: lastMessageSender ?? this.lastMessageSender,
      lastMessagePreview: lastMessagePreview ?? this.lastMessagePreview,
      hasVisitorMsg: hasVisitorMsg ?? this.hasVisitorMsg,
    );
  }

  /// [067] 口径收紧：「已主动联系」严格只信后端 has_visitor_msg 字段。
  /// 与 admin Console.vue isContacted(c) 完全对齐 — 只有访客真实发言（messages.sender='visitor'）
  /// 才算「已联系」；voice 通话事件（含拒接 / 未接 / 取消）和 unread 兜底都不再计入。
  /// 原因：sys 事件曾导致 unread 虚高 → 误判已联系；现已严格收口到访客真消息。
  bool get isContacted => hasVisitorMsg;

  String get displayName => identifier.isNotEmpty
      ? identifier
      : '访客 ${visitorId.length >= 6 ? visitorId.substring(0, 6) : visitorId}';

  String get location {
    final parts = <String>[];
    if (country.isNotEmpty) parts.add(country);
    if (city.isNotEmpty) parts.add(city);
    return parts.join(' · ');
  }

  /// 列表显示用：「我：xxx」/「xxx」/ fallback 到地理位置 / 活动时间
  String get displayPreview {
    if (lastMessagePreview.isNotEmpty) {
      final prefix = lastMessageSender == 'agent' ? '我：' : '';
      return prefix + lastMessagePreview;
    }
    final loc = location;
    if (loc.isNotEmpty) return loc;
    return '';
  }
}

class Message {
  final String id;
  final String convId;
  final String sender; // visitor / agent / sys
  final String senderRef;
  final String content;
  final String mediaUrl;
  final String mediaKind; // image / file
  final String mediaName;
  final DateTime createdAt;
  bool read;
  // 页面跳转专用：sender_ref="page:<url>" 时解析出 URL/title
  final String pageUrl;
  final String pageTitle;

  Message({
    required this.id,
    required this.convId,
    required this.sender,
    required this.senderRef,
    required this.content,
    this.mediaUrl = '',
    this.mediaKind = '',
    this.mediaName = '',
    required this.createdAt,
    this.read = false,
    this.pageUrl = '',
    this.pageTitle = '',
  });

  factory Message.fromJson(Map<String, dynamic> j) {
    String pickStr(dynamic v) {
      if (v == null) return '';
      if (v is String) return v;
      if (v is Map && v['Valid'] == true) return v['String']?.toString() ?? '';
      return '';
    }

    final sender = j['sender']?.toString() ?? '';
    final senderRef = j['sender_ref']?.toString() ?? '';
    String pageUrl = '';
    String pageTitle = '';
    if (sender == 'sys' && senderRef.startsWith('page:')) {
      pageUrl = senderRef.substring(5);
      final content = j['content']?.toString() ?? '';
      final m = RegExp('「(.+?)」').firstMatch(content);
      if (m != null) pageTitle = m.group(1) ?? '';
    }
    return Message(
      id: j['id']?.toString() ?? '',
      convId: j['conv_id']?.toString() ?? '',
      sender: sender,
      senderRef: senderRef,
      content: j['content']?.toString() ?? '',
      mediaUrl: pickStr(j['media_url']),
      mediaKind: pickStr(j['media_kind']),
      mediaName: pickStr(j['media_name']),
      createdAt: _parseDate(j['created_at']),
      read: j['read'] == true,
      pageUrl: pageUrl,
      pageTitle: pageTitle,
    );
  }

  bool get isPageNavigation => sender == 'sys' && pageUrl.isNotEmpty;
  bool get isSys => sender == 'sys';
}

DateTime _parseDate(dynamic v) {
  if (v == null) return DateTime.now();
  if (v is DateTime) return v;
  final s = v.toString();
  // 后端可能返回 "2026-05-21 14:30:01" 或 "2026-05-21T14:30:01+08:00"
  try {
    return DateTime.parse(s.replaceFirst(' ', 'T')).toLocal();
  } catch (_) {
    return DateTime.now();
  }
}
