import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:intl/intl.dart';
import '../api/models.dart';
import '../pages/image_viewer_page.dart';

class MessageBubble extends StatelessWidget {
  final Message msg;
  final bool isMine;
  final bool showRead;
  final String backendUrl;
  const MessageBubble({
    super.key,
    required this.msg,
    required this.isMine,
    required this.backendUrl,
    this.showRead = false,
  });

  @override
  Widget build(BuildContext context) {
    final isSys = msg.isSys && !msg.isPageNavigation;
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 4),
      child: Column(
        crossAxisAlignment: isMine ? CrossAxisAlignment.end : CrossAxisAlignment.start,
        children: [
          // 长按消息气泡 → 复制内容到剪贴板 + SnackBar 提示「已复制」
          // 文本消息复制 content；文件/图片消息复制完整 URL
          GestureDetector(
            onLongPress: () => _copyMessage(context),
            child: Container(
              constraints: BoxConstraints(maxWidth: MediaQuery.of(context).size.width * 0.74),
              padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 9),
              decoration: BoxDecoration(
                color: isMine
                    ? const Color(0xFF2974FF)
                    : (isSys ? const Color(0xFFF3F4F6) : Colors.white),
                borderRadius: BorderRadius.only(
                  topLeft: const Radius.circular(16),
                  topRight: const Radius.circular(16),
                  bottomLeft: Radius.circular(isMine ? 16 : 4),
                  bottomRight: Radius.circular(isMine ? 4 : 16),
                ),
                border: isMine ? null : Border.all(color: const Color(0xFFE5E7EB)),
                boxShadow: const [BoxShadow(color: Color(0x0A0F172A), blurRadius: 2, offset: Offset(0, 1))],
              ),
              child: _content(),
            ),
          ),
          if (showRead)
            Padding(
              padding: const EdgeInsets.only(top: 2, right: 4),
              child: Text('已读', style: TextStyle(fontSize: 11, color: Colors.grey[600])),
            ),
        ],
      ),
    );
  }

  // 长按复制：文本优先，文件/图片复制完整 URL 方便用户在浏览器粘贴打开
  void _copyMessage(BuildContext context) {
    String text = msg.content;
    if (text.isEmpty && msg.mediaUrl.isNotEmpty) {
      text = backendUrl + msg.mediaUrl;
    }
    if (text.isEmpty) return;
    Clipboard.setData(ClipboardData(text: text));
    ScaffoldMessenger.of(context).showSnackBar(
      const SnackBar(
        content: Text('已复制'),
        duration: Duration(milliseconds: 1000),
        behavior: SnackBarBehavior.floating,
      ),
    );
  }

  Widget _content() {
    final hasMedia = msg.mediaUrl.isNotEmpty;
    if (hasMedia && msg.mediaKind == 'image') {
      return Builder(builder: (ctx) => GestureDetector(
        // 点击图片 → 全屏图片查看器 (photo_view: 双指缩放 / 拖动 / 双击放大)
        onTap: () => ImageViewerPage.open(ctx, backendUrl + msg.mediaUrl),
        child: ClipRRect(
          borderRadius: BorderRadius.circular(10),
          child: Image.network(
            backendUrl + msg.mediaUrl,
            width: 220,
            fit: BoxFit.cover,
            errorBuilder: (_, __, ___) => Text(msg.mediaName, style: _textStyle()),
          ),
        ),
      ));
    }
    if (hasMedia) {
      return Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Icon(Icons.attach_file, size: 16, color: isMine ? Colors.white : Colors.grey[800]),
          const SizedBox(width: 6),
          Flexible(child: Text(msg.mediaName.isNotEmpty ? msg.mediaName : '附件', style: _textStyle())),
        ],
      );
    }
    return Text(msg.content, style: _textStyle());
  }

  TextStyle _textStyle() => TextStyle(
        color: isMine ? Colors.white : const Color(0xFF2C3034),
        fontSize: 14,
        height: 1.5,
      );
}

class TimeDivider extends StatelessWidget {
  final DateTime ts;
  const TimeDivider({super.key, required this.ts});

  @override
  Widget build(BuildContext context) {
    final now = DateTime.now();
    final today = DateTime(now.year, now.month, now.day);
    final d = DateTime(ts.year, ts.month, ts.day);
    String label;
    if (d == today) {
      label = '今天 ' + DateFormat('HH:mm').format(ts);
    } else if (d == today.subtract(const Duration(days: 1))) {
      label = '昨天 ' + DateFormat('HH:mm').format(ts);
    } else {
      label = DateFormat('MM-dd HH:mm').format(ts);
    }
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 10),
      child: Center(
        child: Container(
          padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 3),
          decoration: BoxDecoration(
            color: const Color(0xFFE5E7EB),
            borderRadius: BorderRadius.circular(10),
          ),
          child: Text(label, style: const TextStyle(fontSize: 11, color: Color(0xFF6B7280))),
        ),
      ),
    );
  }
}
