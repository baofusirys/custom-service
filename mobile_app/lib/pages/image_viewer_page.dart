import 'package:flutter/material.dart';
import 'package:photo_view/photo_view.dart';

/// 聊天图片全屏查看器。
/// 用 photo_view 实现双指缩放 / 拖动 / 双击放大。
/// 点击空白 / 按返回键关闭，右上角有 × 关闭按钮。
class ImageViewerPage extends StatelessWidget {
  final String url;
  const ImageViewerPage({super.key, required this.url});

  /// 静态推入路由的便捷方法
  static Future<void> open(BuildContext context, String url) {
    return Navigator.of(context).push(
      PageRouteBuilder(
        opaque: false,
        barrierColor: Colors.black87,
        transitionDuration: const Duration(milliseconds: 150),
        pageBuilder: (_, __, ___) => ImageViewerPage(url: url),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: Colors.transparent,
      body: Stack(
        children: [
          // 双击 / 缩放 / 拖动；点空白处不关闭（避免误触）
          PhotoView(
            imageProvider: NetworkImage(url),
            backgroundDecoration: const BoxDecoration(color: Colors.transparent),
            minScale: PhotoViewComputedScale.contained,
            maxScale: PhotoViewComputedScale.covered * 3,
            loadingBuilder: (context, event) => const Center(
              child: CircularProgressIndicator(color: Colors.white),
            ),
            errorBuilder: (_, __, ___) => const Center(
              child: Icon(Icons.broken_image, color: Colors.white54, size: 64),
            ),
          ),
          // 右上角关闭按钮
          Positioned(
            top: MediaQuery.of(context).padding.top + 8,
            right: 8,
            child: Material(
              color: Colors.transparent,
              child: IconButton(
                icon: const Icon(Icons.close, color: Colors.white, size: 28),
                style: IconButton.styleFrom(
                  backgroundColor: Colors.white.withValues(alpha: 0.18),
                  padding: const EdgeInsets.all(8),
                ),
                onPressed: () => Navigator.of(context).pop(),
              ),
            ),
          ),
        ],
      ),
    );
  }
}
