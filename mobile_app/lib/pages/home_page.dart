import 'package:flutter/material.dart';
import 'package:provider/provider.dart';
import '../state/app_state.dart';
import '../widgets/glass.dart';
import '../widgets/voice_call_overlay.dart';
import 'conversations_page.dart';
import 'me_page.dart';

/// 主框架：底部 Tab。
///   - 在线会话
///   - 我的（含切换服务器 / 退出 / 后续 第 2 批加：历史、设置、客服管理）
///
/// 进入主框架时启动 WSS，离开（登出）时停止。
class HomePage extends StatefulWidget {
  const HomePage({super.key});

  @override
  State<HomePage> createState() => _HomePageState();
}

class _HomePageState extends State<HomePage> {
  int _tab = 0;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) {
      final s = context.read<AppState>();
      s.startWs();
      s.refreshConvs();
      s.loadAgentSound();
    });
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: const Color(0xFFF2F2F7),
      extendBody: true,
      // Stack：内容 + 浮动玻璃 tab bar + VoiceCallOverlay 都浮在内容之上
      body: Stack(
        children: [
          IndexedStack(
            index: _tab,
            children: const [
              ConversationsPage(),
              MePage(),
            ],
          ),
          // [074] iOS 26 浮动玻璃 tab bar：胶囊形浮在内容上，两边留白 + 柔和阴影
          Positioned(
            left: 0,
            right: 0,
            bottom: MediaQuery.of(context).padding.bottom + 10,
            child: Center(child: _floatingTabBar()),
          ),
          const VoiceCallOverlay(),
        ],
      ),
    );
  }

  /// [074] iOS 26 浮动玻璃胶囊 tab bar：选中项是蓝色胶囊（图标+文字），未选中只图标。
  Widget _floatingTabBar() {
    const items = [
      (Icons.chat_bubble_outline, Icons.chat_bubble, '会话'),
      (Icons.person_outline, Icons.person, '我的'),
    ];
    return GlassBar(
      borderRadius: BorderRadius.circular(28),
      padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 6),
      shadow: const [
        BoxShadow(color: Color(0x1F000000), blurRadius: 20, offset: Offset(0, 6)),
      ],
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: List.generate(items.length, (i) {
          final sel = _tab == i;
          final it = items[i];
          return GestureDetector(
            onTap: () => setState(() => _tab = i),
            behavior: HitTestBehavior.opaque,
            child: AnimatedContainer(
              duration: const Duration(milliseconds: 200),
              padding: const EdgeInsets.symmetric(horizontal: 18, vertical: 9),
              decoration: BoxDecoration(
                color: sel ? const Color(0xFF2974FF) : Colors.transparent,
                borderRadius: BorderRadius.circular(22),
              ),
              child: Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Icon(sel ? it.$2 : it.$1,
                      size: 20, color: sel ? Colors.white : const Color(0xFF6B7280)),
                  if (sel) ...[
                    const SizedBox(width: 6),
                    Text(it.$3,
                        style: const TextStyle(
                            color: Colors.white, fontSize: 13, fontWeight: FontWeight.w600)),
                  ],
                ],
              ),
            ),
          );
        }),
      ),
    );
  }
}
