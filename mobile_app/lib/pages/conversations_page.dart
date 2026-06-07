import 'package:flutter/material.dart';
import 'package:intl/intl.dart';
import 'package:provider/provider.dart';
import '../api/models.dart';
import '../state/app_state.dart';
import '../widgets/glass.dart';
import 'chat_page.dart';

class ConversationsPage extends StatefulWidget {
  const ConversationsPage({super.key});

  @override
  State<ConversationsPage> createState() => _ConversationsPageState();
}

class _ConversationsPageState extends State<ConversationsPage> with WidgetsBindingObserver {
  AppState? _state;

  @override
  void initState() {
    super.initState();
    // [050] 监听 App 生命周期：从后台切回前台时自动 refreshConvs 拉最新会话列表 + 各最近消息
    WidgetsBinding.instance.addObserver(this);
    // [050] 进入这个页面立刻拉一次（不依赖 HomePage initState 只拉一次的旧逻辑）
    WidgetsBinding.instance.addPostFrameCallback((_) {
      _state?.refreshConvs();
    });
  }

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    _state = context.read<AppState>();
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    super.dispose();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState s) {
    // [050] App 从后台切回前台 → 自动拉最新会话列表（防爷爷 App 一打开看到旧数据）
    if (s == AppLifecycleState.resumed) {
      _state?.refreshConvs();
    }
  }

  @override
  Widget build(BuildContext context) {
    final state = context.watch<AppState>();
    // [066] 同步 [065] admin Console「全部 / 已联系」过滤 tab。
    final totalCount = state.convs.length;
    final contactedCount = state.contactedCount;
    final filterMode = state.filterMode.value;
    final filtered = state.filteredConvs;
    final isContactedMode = filterMode == 'contacted';

    return Scaffold(
      backgroundColor: const Color(0xFFF2F2F7),
      appBar: AppBar(
        backgroundColor: Colors.transparent,
        elevation: 0,
        scrolledUnderElevation: 0,
        // [074] iOS 26 玻璃顶栏
        flexibleSpace: const GlassBar(border: false, child: SizedBox.expand()),
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
        // [066] Material 3 原生 SegmentedButton，不写任何自定义样式
        bottom: PreferredSize(
          preferredSize: const Size.fromHeight(52),
          child: Padding(
            padding: const EdgeInsets.fromLTRB(12, 0, 12, 8),
            child: SizedBox(
              width: double.infinity,
              child: SegmentedButton<String>(
                segments: [
                  ButtonSegment<String>(
                    value: 'all',
                    label: Text('全部 ($totalCount)'),
                    icon: const Icon(Icons.list_alt),
                  ),
                  ButtonSegment<String>(
                    value: 'contacted',
                    label: Text('已联系 ($contactedCount)'),
                    icon: const Icon(Icons.chat_bubble_outline),
                  ),
                ],
                selected: <String>{filterMode},
                showSelectedIcon: false,
                onSelectionChanged: (s) {
                  if (s.isNotEmpty) state.setFilterMode(s.first);
                },
              ),
            ),
          ),
        ),
      ),
      body: RefreshIndicator(
        onRefresh: () => state.refreshConvs(),
        child: filtered.isEmpty
            ? ListView(
                children: [
                  const SizedBox(height: 160),
                  const Center(child: Icon(Icons.inbox_outlined, size: 64, color: Colors.grey)),
                  const SizedBox(height: 12),
                  Center(
                    child: Text(
                      isContactedMode
                          ? '暂无已联系访客（访客发首条消息后会出现）'
                          : '暂无进行中的会话',
                      style: const TextStyle(color: Colors.grey),
                    ),
                  ),
                ],
              )
            : ListView.separated(
                // [074] 卡片间距 8 + 底部 96 给浮动 tab bar 让位
                padding: const EdgeInsets.fromLTRB(12, 8, 12, 96),
                itemCount: filtered.length,
                separatorBuilder: (_, __) => const SizedBox(height: 8),
                itemBuilder: (ctx, i) => _convTile(context, filtered[i]),
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
    // [074] iOS 26 内容层：圆角白卡片（squircle 半径 16），玻璃只在 chrome、内容用白卡
    return Container(
      decoration: BoxDecoration(
        color: Colors.white,
        borderRadius: BorderRadius.circular(16),
      ),
      clipBehavior: Clip.antiAlias,
      child: ListTile(
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
      onTap: () {
        // [051] 立刻 push 页面 + 后台拉消息（不 await），实现 IM 标准的 0ms 切换体验
        // 改前：await openConv → 等 1-2s HTTP → push，用户感觉"卡了一下"
        // 改后：openConv 立刻设 activeConv + 触发后台 HTTP，立刻 push 页面
        //       ChatPage 通过 state.loadingMessages 显示 spinner，HTTP 返回后自动重渲染
        ctx.read<AppState>().openConv(c);
        Navigator.of(ctx).push(MaterialPageRoute(builder: (_) => const ChatPage()));
      },
      ),
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
