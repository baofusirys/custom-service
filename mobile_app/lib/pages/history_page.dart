import 'package:flutter/material.dart';
import 'package:intl/intl.dart';
import 'package:provider/provider.dart';
import '../api/http_client.dart';
import '../api/models.dart';
import '../state/app_state.dart';
import 'chat_page.dart';

/// 历史记录页：当前后端没有专门的 closed 会话接口，先复用 /agent/conversations
/// 后续可扩展为 /agent/conversations/history（含 closed）
class HistoryPage extends StatefulWidget {
  const HistoryPage({super.key});

  @override
  State<HistoryPage> createState() => _HistoryPageState();
}

class _HistoryPageState extends State<HistoryPage> {
  bool _loading = true;
  List<Conversation> _list = [];

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    setState(() => _loading = true);
    try {
      final raw = await Api.listConversations();
      _list = raw.map(Conversation.fromJson).toList();
    } catch (_) {}
    if (mounted) setState(() => _loading = false);
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text('历史记录'),
        actions: [
          IconButton(icon: const Icon(Icons.refresh), onPressed: _load),
        ],
      ),
      body: _loading
          ? const Center(child: CircularProgressIndicator())
          : _list.isEmpty
              ? const Center(child: Text('暂无记录', style: TextStyle(color: Colors.grey)))
              : ListView.separated(
                  itemCount: _list.length,
                  separatorBuilder: (_, __) => const Divider(height: 1, indent: 16),
                  itemBuilder: (ctx, i) {
                    final c = _list[i];
                    return ListTile(
                      title: Text(c.displayName,
                          maxLines: 1, overflow: TextOverflow.ellipsis,
                          style: const TextStyle(fontWeight: FontWeight.w600)),
                      subtitle: Text(
                        c.displayPreview.isNotEmpty ? c.displayPreview : '最近活动 · ${_fmt(c.updatedAt)}',
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: const TextStyle(fontSize: 12),
                      ),
                      trailing: Text(_fmt(c.updatedAt),
                          style: TextStyle(fontSize: 11, color: Colors.grey[600])),
                      onTap: () {
                        // [051] 同 conversations_page：openConv 已改为同步 void，立刻 push 不 await
                        context.read<AppState>().openConv(c);
                        Navigator.of(context).push(MaterialPageRoute(builder: (_) => const ChatPage()));
                      },
                    );
                  },
                ),
    );
  }

  String _fmt(DateTime t) {
    final now = DateTime.now();
    if (t.year == now.year && t.month == now.month && t.day == now.day) {
      return DateFormat('HH:mm').format(t);
    }
    if (t.year == now.year) return DateFormat('MM-dd HH:mm').format(t);
    return DateFormat('yyyy-MM-dd').format(t);
  }
}
