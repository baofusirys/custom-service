import 'dart:io';
import 'package:file_picker/file_picker.dart';
import 'package:flutter/material.dart';
import 'package:image_picker/image_picker.dart';
import 'package:provider/provider.dart';
import '../api/models.dart';
import '../state/app_state.dart';
import '../widgets/glass.dart';
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
  // [041] 待发送文件队列（拍照 / 相册多选 / 文件追加，点发送时依次上传）
  final List<File> _pending = [];
  bool _sending = false;
  final _picker = ImagePicker();

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

  /// [041] 发送：先发文本（如果有），再依次上传 _pending 队列里的所有附件
  /// 任一存在就算可发；上传期间禁按发送防重复点
  Future<void> _send() async {
    if (_sending) return;
    final t = _input.text.trim();
    final files = List<File>.from(_pending);
    if (t.isEmpty && files.isEmpty) return;
    final app = context.read<AppState>();
    setState(() => _sending = true);
    try {
      if (t.isNotEmpty) {
        await app.sendChat(t);
        _input.clear();
      }
      if (files.isNotEmpty) {
        // 先清 UI 防重复点；失败的不重发，用户可重新选
        setState(() => _pending.clear());
        for (final f in files) {
          final ok = await app.uploadAndSendFile(f);
          if (!ok && mounted) {
            ScaffoldMessenger.of(context).showSnackBar(
              SnackBar(content: Text('上传失败：${f.path.split(Platform.pathSeparator).last}')),
            );
          }
        }
      }
    } finally {
      if (mounted) setState(() => _sending = false);
    }
    WidgetsBinding.instance.addPostFrameCallback((_) => _scrollToBottom());
  }

  /// [041] 弹底部菜单：拍照 / 相册多选 / 任意文件
  Future<void> _pickAttachment() async {
    final action = await showModalBottomSheet<String>(
      context: context,
      builder: (ctx) => SafeArea(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            ListTile(
              leading: const Icon(Icons.photo_camera_outlined),
              title: const Text('拍照'),
              onTap: () => Navigator.pop(ctx, 'camera'),
            ),
            ListTile(
              leading: const Icon(Icons.photo_library_outlined),
              title: const Text('从相册选（可多选）'),
              onTap: () => Navigator.pop(ctx, 'gallery'),
            ),
            ListTile(
              leading: const Icon(Icons.insert_drive_file_outlined),
              title: const Text('选择文件'),
              onTap: () => Navigator.pop(ctx, 'file'),
            ),
          ],
        ),
      ),
    );
    if (action == null) return;
    try {
      if (action == 'camera') {
        final x = await _picker.pickImage(source: ImageSource.camera, imageQuality: 88);
        if (x != null) setState(() => _pending.add(File(x.path)));
      } else if (action == 'gallery') {
        final xs = await _picker.pickMultiImage(imageQuality: 88);
        if (xs.isNotEmpty) {
          setState(() => _pending.addAll(xs.map((x) => File(x.path))));
        }
      } else if (action == 'file') {
        final res = await FilePicker.platform.pickFiles(allowMultiple: true, withData: false);
        if (res != null) {
          final picked = res.files.where((f) => f.path != null).map((f) => File(f.path!));
          setState(() => _pending.addAll(picked));
        }
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('选择失败：$e')));
      }
    }
  }

  void _removePending(File f) {
    setState(() => _pending.remove(f));
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

    // [074] iOS 26 官方风格：浅色内容层（系统灰 #F2F2F7），玻璃只用在顶栏/输入栏（chrome）。
    return Scaffold(
      backgroundColor: const Color(0xFFF2F2F7),
      appBar: AppBar(
        backgroundColor: Colors.transparent,
        elevation: 0,
        scrolledUnderElevation: 0,
        centerTitle: true,
        // [074] 玻璃顶栏：GlassBar 充满 AppBar 区域（含状态栏），标题/返回浮在上面
        flexibleSpace: const GlassBar(border: false, child: SizedBox.expand()),
        leading: IconButton(
          icon: const Icon(Icons.arrow_back_ios_new, size: 20, color: Color(0xFF1C1C1E)),
          onPressed: () => Navigator.of(context).maybePop(),
        ),
        // [037] 标题真居中
        title: Text(
          conv.displayName,
          style: const TextStyle(fontSize: 17, fontWeight: FontWeight.w600, color: Color(0xFF1C1C1E)),
        ),
      ),
      body: Column(
        children: [
          // [037] 访客来源 / 当前页 / 位置信息条（对齐 admin web Console.vue 的访客信息）
          // AppBar 顶部容易被截断看不清，单独放到正文顶部，每条独立一行 + 自动换行，URL 再长也能看清
          _visitorInfoBar(conv),
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
              // [051] 加 loading 态：openConv 立刻 push 页面后，messages 暂时空，显示 spinner
              //       而不是空白页；HTTP 拉到消息后 notifyListeners 自动重渲染
              child: (msgs.isEmpty && state.loadingMessages)
                  ? const Center(
                      child: Column(
                        mainAxisSize: MainAxisSize.min,
                        children: [
                          SizedBox(
                            width: 32, height: 32,
                            child: CircularProgressIndicator(strokeWidth: 2.5),
                          ),
                          SizedBox(height: 12),
                          Text('加载消息中…', style: TextStyle(color: Colors.grey, fontSize: 13)),
                        ],
                      ),
                    )
                  : ListView.builder(
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

  /// 访客信息条：来源 / 当前页 / 地理位置，对齐 admin Console.vue 的访客信息块
  /// 三项任一非空才显示；URL 太长支持换行而不是 ellipsis（爷爷反馈"看不清"）
  Widget _visitorInfoBar(Conversation conv) {
    final rows = <Widget>[];
    if (conv.referer.isNotEmpty) {
      rows.add(_infoLine('来源', conv.referer));
    }
    if (conv.lastPage.isNotEmpty) {
      rows.add(_infoLine('当前页', conv.lastPage));
    }
    if (conv.location.isNotEmpty) {
      rows.add(_infoLine('位置', conv.location));
    }
    if (rows.isEmpty) return const SizedBox.shrink();
    return Container(
      width: double.infinity,
      color: const Color(0xFFF5F7FA),
      padding: const EdgeInsets.fromLTRB(12, 8, 12, 8),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: rows,
      ),
    );
  }

  Widget _infoLine(String label, String value) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 2),
      child: RichText(
        text: TextSpan(
          style: const TextStyle(fontSize: 13, color: Color(0xFF606266), height: 1.4),
          children: [
            TextSpan(
              text: '$label：',
              style: const TextStyle(fontWeight: FontWeight.w600, color: Color(0xFF303133)),
            ),
            TextSpan(text: value),
          ],
        ),
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
    // [074] 输入栏玻璃化（iOS26 chrome 层）：克制毛玻璃 + 发丝边，浮在浅色内容上
    return GlassBar(
      padding: EdgeInsets.only(
        left: 12, right: 12, top: 10,
        bottom: 10 + MediaQuery.of(context).padding.bottom,
      ),
      child: SafeArea(
        top: false,
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            if (_pending.isNotEmpty) _pendingBar(),
            Row(
              children: [
                IconButton(
                  tooltip: '添加图片 / 文件',
                  onPressed: _sending ? null : _pickAttachment,
                  icon: const Icon(Icons.add_circle_outline, size: 28, color: Color(0xFF2974FF)),
                ),
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
                FilledButton(
                  onPressed: _sending ? null : _send,
                  child: _sending
                      ? const SizedBox(width: 16, height: 16, child: CircularProgressIndicator(strokeWidth: 2, color: Colors.white))
                      : const Text('发送'),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }

  /// [041] 待发送文件 chip 队列：缩略图（图片）/ 文件图标，长按或 × 移除
  Widget _pendingBar() {
    return Container(
      padding: const EdgeInsets.only(bottom: 8),
      child: Wrap(
        spacing: 6, runSpacing: 6,
        children: _pending.map(_pendingChip).toList(),
      ),
    );
  }

  Widget _pendingChip(File f) {
    final name = f.path.split(Platform.pathSeparator).last;
    final lname = name.toLowerCase();
    final isImage = lname.endsWith('.jpg') || lname.endsWith('.jpeg') ||
        lname.endsWith('.png') || lname.endsWith('.gif') ||
        lname.endsWith('.webp') || lname.endsWith('.heic');
    return Container(
      constraints: const BoxConstraints(maxWidth: 200),
      decoration: BoxDecoration(
        color: Colors.white,
        border: Border.all(color: const Color(0xFFE5E7EB)),
        borderRadius: BorderRadius.circular(8),
      ),
      padding: const EdgeInsets.fromLTRB(4, 4, 4, 4),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          if (isImage)
            ClipRRect(
              borderRadius: BorderRadius.circular(5),
              child: Image.file(f, width: 36, height: 36, fit: BoxFit.cover),
            )
          else
            Container(
              width: 30, height: 30,
              decoration: BoxDecoration(
                color: const Color(0xFFEEF4FF),
                borderRadius: BorderRadius.circular(5),
              ),
              child: const Icon(Icons.insert_drive_file_outlined, size: 18, color: Color(0xFF2974FF)),
            ),
          const SizedBox(width: 6),
          Flexible(
            child: Text(
              name,
              maxLines: 1, overflow: TextOverflow.ellipsis,
              style: const TextStyle(fontSize: 12, color: Color(0xFF2C3034)),
            ),
          ),
          const SizedBox(width: 4),
          InkWell(
            onTap: () => _removePending(f),
            borderRadius: BorderRadius.circular(10),
            child: const Padding(
              padding: EdgeInsets.all(2),
              child: Icon(Icons.close, size: 16, color: Color(0xFF9CA3AF)),
            ),
          ),
        ],
      ),
    );
  }
}
