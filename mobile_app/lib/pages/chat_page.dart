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
  bool _autoScroll = true;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => _scrollToBottom());
  }

  @override
  void dispose() {
    _input.dispose();
    _scroll.dispose();
    context.read<AppState>().closeActive();
    super.dispose();
  }

  void _scrollToBottom() {
    if (!_scroll.hasClients) return;
    _scroll.jumpTo(_scroll.position.maxScrollExtent);
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
    // 收到新消息时尝试自动滚到底（用户没有手动往上看的话）
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
                  final pos = n.metrics;
                  _autoScroll = pos.maxScrollExtent - pos.pixels < 50;
                }
                return false;
              },
              child: ListView.builder(
                controller: _scroll,
                padding: const EdgeInsets.only(top: 8, bottom: 12),
                itemCount: msgs.length,
                itemBuilder: (ctx, i) => _row(msgs[i], i == 0 ? null : msgs[i - 1], lastMine, state),
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
