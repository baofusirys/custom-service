import 'package:flutter/material.dart';
import 'package:intl/intl.dart';
import 'package:provider/provider.dart';
import '../api/models.dart';
import '../state/app_state.dart';
import 'chat_page.dart';

class ConversationsPage extends StatelessWidget {
  const ConversationsPage({super.key});

  @override
  Widget build(BuildContext context) {
    final state = context.watch<AppState>();
    return Scaffold(
      appBar: AppBar(
        title: Row(
          children: [
            const Text('在线会话'),
            const SizedBox(width: 8),
            _wsBadge(state.wsAlive),
          ],
        ),
        actions: [
          IconButton(icon: const Icon(Icons.refresh), onPressed: () => state.refreshConvs()),
        ],
      ),
      body: RefreshIndicator(
        onRefresh: () => state.refreshConvs(),
        child: state.convs.isEmpty
            ? ListView(
                children: const [
                  SizedBox(height: 160),
                  Center(child: Icon(Icons.inbox_outlined, size: 64, color: Colors.grey)),
                  SizedBox(height: 12),
                  Center(child: Text('暂无进行中的会话', style: TextStyle(color: Colors.grey))),
                ],
              )
            : ListView.separated(
                itemCount: state.convs.length,
                separatorBuilder: (_, __) => const Divider(height: 1, indent: 72),
                itemBuilder: (ctx, i) => _convTile(context, state.convs[i]),
              ),
      ),
    );
  }

  Widget _wsBadge(bool alive) {
    return Container(
      width: 8,
      height: 8,
      decoration: BoxDecoration(
        shape: BoxShape.circle,
        color: alive ? Colors.green : Colors.red,
      ),
    );
  }

  Widget _convTile(BuildContext ctx, Conversation c) {
    final name = c.displayName;
    final color = _avatarColor(c.visitorId);
    final initial = (name.isNotEmpty ? name[0] : '?').toUpperCase();
    return ListTile(
      leading: CircleAvatar(
        backgroundColor: color,
        child: Text(initial, style: const TextStyle(color: Colors.white, fontWeight: FontWeight.w600)),
      ),
      title: Row(
        children: [
          Expanded(child: Text(name, maxLines: 1, overflow: TextOverflow.ellipsis, style: const TextStyle(fontWeight: FontWeight.w600))),
          Text(_fmt(c.updatedAt), style: TextStyle(fontSize: 12, color: Colors.grey[600])),
        ],
      ),
      subtitle: Row(
        children: [
          Expanded(
            child: Text(
              c.displayPreview.isNotEmpty
                  ? c.displayPreview
                  : '最近活动 · ${_fmt(c.updatedAt)}',
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: const TextStyle(fontSize: 12),
            ),
          ),
          if (c.unread > 0)
            Container(
              margin: const EdgeInsets.only(left: 8),
              padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
              decoration: BoxDecoration(
                color: Colors.red,
                borderRadius: BorderRadius.circular(10),
              ),
              constraints: const BoxConstraints(minWidth: 18),
              child: Text(
                c.unread > 99 ? '99+' : c.unread.toString(),
                style: const TextStyle(color: Colors.white, fontSize: 11, fontWeight: FontWeight.w600),
                textAlign: TextAlign.center,
              ),
            ),
        ],
      ),
      onTap: () async {
        await ctx.read<AppState>().openConv(c);
        if (ctx.mounted) {
          Navigator.of(ctx).push(MaterialPageRoute(builder: (_) => const ChatPage()));
        }
      },
    );
  }

  String _fmt(DateTime t) {
    final now = DateTime.now();
    if (t.year == now.year && t.month == now.month && t.day == now.day) {
      return DateFormat('HH:mm').format(t);
    }
    if (t.year == now.year) {
      return DateFormat('MM-dd HH:mm').format(t);
    }
    return DateFormat('yyyy-MM-dd').format(t);
  }

  Color _avatarColor(String id) {
    const colors = [
      Color(0xFF67C23A), Color(0xFFE6A23C), Color(0xFFF56C6C),
      Color(0xFF409EFF), Color(0xFF9C27B0), Color(0xFF00BCD4),
      Color(0xFFFF9800),
    ];
    if (id.isEmpty) return Colors.grey;
    var h = 0;
    for (final c in id.codeUnits) {
      h = ((h * 31) + c) & 0xFFFFFFFF;
    }
    return colors[h.abs() % colors.length];
  }
}
