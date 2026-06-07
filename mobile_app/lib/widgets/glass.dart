import 'dart:ui';
import 'package:flutter/material.dart';

/// [074] iOS 26 风格自写玻璃组件 —— 不依赖第三方 shader 库，纯 Flutter 原生 BackdropFilter。
///
/// 设计依据（Apple HIG / WWDC25 深度研究结论）：
///   - 玻璃只用在「导航/控件层」（chrome）：顶栏、底部 tab、输入栏、浮动按钮；
///     绝不用于内容层（消息列表、气泡、页面背景）。
///   - 克制、薄、让内容透过：blur sigma ~24、薄白遮罩 ~0.6、0.5px 发丝边 white@0.18。
///   - 内容层保持浅色不透明（系统灰 #F2F2F7 / 白卡片），靠层次而非彩色背景出效果。
class GlassBar extends StatelessWidget {
  final Widget child;
  final BorderRadius borderRadius;
  final EdgeInsetsGeometry? padding;
  final double blurSigma;
  final Color tint;
  final bool border;
  final List<BoxShadow>? shadow;

  const GlassBar({
    super.key,
    required this.child,
    this.borderRadius = BorderRadius.zero,
    this.padding,
    this.blurSigma = 24,
    this.tint = const Color(0x99FFFFFF), // white @ 0.60，薄中性遮罩
    this.border = true,
    this.shadow,
  });

  @override
  Widget build(BuildContext context) {
    Widget glass = ClipRRect(
      borderRadius: borderRadius,
      child: BackdropFilter(
        filter: ImageFilter.blur(sigmaX: blurSigma, sigmaY: blurSigma),
        child: Container(
          padding: padding,
          decoration: BoxDecoration(
            color: tint,
            borderRadius: borderRadius,
            // 0.5px 发丝高光边 white@0.18 —— iOS26 玻璃边缘的微光
            border: border
                ? Border.all(color: const Color(0x2EFFFFFF), width: 0.5)
                : null,
          ),
          child: child,
        ),
      ),
    );
    // 浮动玻璃（如底部 tab）需要柔和阴影把它从内容上「抬起来」
    if (shadow != null) {
      glass = DecoratedBox(
        decoration: BoxDecoration(borderRadius: borderRadius, boxShadow: shadow),
        child: glass,
      );
    }
    return glass;
  }
}
