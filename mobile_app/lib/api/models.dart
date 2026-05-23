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
  });

  factory Conversation.fromJson(Map<String, dynamic> j) => Conversation(
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
      );

  String get displayName => identifier.isNotEmpty
      ? identifier
      : '访客 ${visitorId.length >= 6 ? visitorId.substring(0, 6) : visitorId}';

  String get location {
    final parts = <String>[];
    if (country.isNotEmpty) parts.add(country);
    if (city.isNotEmpty) parts.add(city);
    return parts.join(' · ');
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
