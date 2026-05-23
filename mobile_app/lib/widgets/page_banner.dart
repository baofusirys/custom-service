import 'package:flutter/material.dart';
import '../api/models.dart';

/// 「访客访问了 XXX」橙色横幅（参考 Crisp）
class PageBanner extends StatelessWidget {
  final Message msg;
  const PageBanner({super.key, required this.msg});

  @override
  Widget build(BuildContext context) {
    final title = msg.pageTitle.isNotEmpty ? msg.pageTitle : msg.pageUrl;
    return Container(
      margin: const EdgeInsets.symmetric(vertical: 6, horizontal: 12),
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
      decoration: BoxDecoration(
        color: const Color(0xFFFFF7ED),
        border: Border.all(color: const Color(0xFFFED7AA)),
        borderRadius: BorderRadius.circular(14),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          const Icon(Icons.arrow_forward, size: 14, color: Color(0xFFF97316)),
          const SizedBox(width: 6),
          const Text(
            '访客访问了',
            style: TextStyle(color: Color(0xFFC2410C), fontSize: 12),
          ),
          const SizedBox(width: 4),
          Flexible(
            child: Text(
              title,
              style: const TextStyle(
                color: Color(0xFFC2410C),
                fontSize: 12,
                decoration: TextDecoration.underline,
              ),
              maxLines: 2,
              overflow: TextOverflow.ellipsis,
            ),
          ),
        ],
      ),
    );
  }
}
