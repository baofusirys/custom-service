import 'package:flutter/material.dart';
import 'package:provider/provider.dart';
import '../api/models.dart';
import '../state/app_state.dart';
import '../widgets/message_bubble.dart';
import '../widgets/page_banner.dart';

class ChatPage extends StatefulWidget {
  const ChatPage({super.key});

  @override
  State<ChatPage> createState() => _ChatPageState();
}

class _ChatPageState extends State<ChatPage> {
  final _input = TextEditingController();
  final _scroll = ScrollController();
  // 用户当前是否处于「底部附近」—— 在底部时新消息自动滚到底，
  // 用户主动往上看历史时不强制滚动
  bool _autoScroll = true;
  // 缓存 AppState 引用，避免 dispose 时通过已 deactivated 的 context 查找 ancestor
  AppState? _state;

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    _state = context.read<AppState>();
  }

  @override
  void dispose() {
    _input.dispose();
    _scroll.dispose();
    _state?.closeActive();
    super.dispose();
  }

  /// reverse: true 模式下，pixels=0 就是视觉上的底部。
  /// 用 jumpTo(0) 而不是 maxScrollExtent，避免 ListView.builder 懒渲染时算不准的问题。
  void _scrollToBottom() {
    if (!_scroll.hasClients) return;
    _scroll.jumpTo(0);
  }

  void _send() {
    final t = _input.text;
    if (t.trim().isEmpty) return;
    context.read<AppState>().sendChat(t);
    _input.clear();
    WidgetsBinding.instance.addPostFrameCallback((_) => _scrollToBottom());
  }

  @override
  Widget build(BuildContext context) {
    final state = context.watch<AppState>();
    final conv = state.activeConv;
    if (conv == null) {
      return const Scaffold(body: Center(child: Text('未选中会话')));
    }
    final msgs = state.messages;
    final lastMine = _lastMineMsg(msgs, state.agent?.id ?? -1);
    // 新消息到来时，若用户当前在底部附近，自动跟随到底
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (_autoScroll) _scrollToBottom();
    });

    return Scaffold(
      appBar: AppBar(
        title: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(conv.displayName, style: const TextStyle(fontSize: 16)),
            if (conv.location.isNotEmpty || conv.referer.isNotEmpty)
              Text(
                [conv.location, conv.referer].where((s) => s.isNotEmpty).join(' · '),
                style: const TextStyle(fontSize: 11, color: Colors.white70),
              ),
          ],
        ),
      ),
      body: Column(
        children: [
          Expanded(
            child: NotificationListener<ScrollNotification>(
              onNotification: (n) {
                if (n is ScrollEndNotification) {
                  // reverse: true 模式下，pixels=0 就是底部，pixels 增大时往历史方向走
                  _autoScroll = n.metrics.pixels < 50;
                }
                return false;
              },
              // reverse: true —— 进入页面时天然显示最新消息（在视觉底部），
              // 不再依赖 maxScrollExtent 计算，告别 ListView 懒渲染滚不到底的坑
              child: ListView.builder(
                controller: _scroll,
                reverse: true,
                padding: const EdgeInsets.only(top: 12, bottom: 8),
                itemCount: msgs.length,
                itemBuilder: (ctx, i) {
                  // i 是从底往上的 index；映射到原数组的真实索引
                  final realIdx = msgs.length - 1 - i;
                  final m = msgs[realIdx];
                  final prev = realIdx == 0 ? null : msgs[realIdx - 1];
                  return _row(m, prev, lastMine, state);
                },
              ),
            ),
          ),
          _inputBar(),
        ],
      ),
    );
  }

  Widget _row(Message m, Message? prev, Message? lastMine, AppState state) {
    final myId = state.agent?.id.toString() ?? '';
    final isMine = m.sender == 'agent' && m.senderRef == myId;
    final showRead = isMine && lastMine != null && lastMine.id == m.id && lastMine.read;

    final children = <Widget>[];
    if (prev == null || m.createdAt.difference(prev.createdAt).inMinutes >= 5) {
      children.add(TimeDivider(ts: m.createdAt));
    }
    if (m.isPageNavigation) {
      children.add(PageBanner(msg: m));
    } else {
      children.add(MessageBubble(
        msg: m,
        isMine: isMine,
        showRead: showRead,
        backendUrl: state.backendUrl ?? '',
      ));
    }
    return Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: children);
  }

  Message? _lastMineMsg(List<Message> list, int myId) {
    final s = myId.toString();
    for (var i = list.length - 1; i >= 0; i--) {
      final m = list[i];
      if (m.sender == 'agent' && m.senderRef == s) return m;
    }
    return null;
  }

  Widget _inputBar() {
    return Container(
      padding: EdgeInsets.only(
        left: 12, right: 12, top: 8,
        bottom: 8 + MediaQuery.of(context).padding.bottom,
      ),
      decoration: const BoxDecoration(
        color: Colors.white,
        border: Border(top: BorderSide(color: Color(0xFFE5E7EB))),
      ),
      child: SafeArea(
        top: false,
        child: Row(
          children: [
            Expanded(
              child: TextField(
                controller: _input,
                minLines: 1,
                maxLines: 4,
                textInputAction: TextInputAction.send,
                onSubmitted: (_) => _send(),
                decoration: const InputDecoration(
                  hintText: '输入消息',
                  border: OutlineInputBorder(borderRadius: BorderRadius.all(Radius.circular(20))),
                  contentPadding: EdgeInsets.symmetric(horizontal: 14, vertical: 10),
                  isDense: true,
                ),
              ),
            ),
            const SizedBox(width: 8),
            FilledButton(onPressed: _send, child: const Text('发送')),
          ],
        ),
      ),
    );
  }
}
