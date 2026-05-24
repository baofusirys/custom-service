import 'package:flutter/material.dart';
import 'package:provider/provider.dart';
import '../state/app_state.dart';
import '../state/voice_controller.dart';

/// 全局语音通话浮窗：被 HomePage 包在 Stack 顶层，监听 voice.state
/// 自动显示/隐藏来电 / 通话中 / 结束三种状态。
class VoiceCallOverlay extends StatelessWidget {
  const VoiceCallOverlay({super.key});

  @override
  Widget build(BuildContext context) {
    final voice = context.watch<AppState>().voice;
    return AnimatedBuilder(
      animation: voice,
      builder: (context, _) {
        if (voice.state == VoiceState.idle) return const SizedBox.shrink();
        final isIncoming = voice.state == VoiceState.incoming;
        final isTalking = voice.state == VoiceState.talking;
        final isEnded = voice.state == VoiceState.ended;
        return Positioned.fill(
          child: Material(
            color: Colors.black54,
            child: Center(
              child: Container(
                width: 300,
                padding: const EdgeInsets.fromLTRB(24, 28, 24, 22),
                decoration: BoxDecoration(
                  borderRadius: BorderRadius.circular(16),
                  gradient: LinearGradient(
                    begin: Alignment.topLeft,
                    end: Alignment.bottomRight,
                    colors: isTalking
                        ? const [Color(0xFF064E3B), Color(0xFF10B981)]
                        : isEnded
                            ? const [Color(0xFF334155), Color(0xFF64748B)]
                            : const [Color(0xFF1E3A8A), Color(0xFF2974FF)],
                  ),
                ),
                child: Column(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    _PulseIcon(animate: isIncoming),
                    const SizedBox(height: 16),
                    Text(
                      voice.callerLabel.isEmpty ? '访客' : voice.callerLabel,
                      style: const TextStyle(
                        color: Colors.white, fontSize: 18, fontWeight: FontWeight.w600,
                      ),
                    ),
                    const SizedBox(height: 4),
                    Text(
                      voice.statusText,
                      style: const TextStyle(color: Colors.white70, fontSize: 13),
                    ),
                    const SizedBox(height: 24),
                    _buildActions(voice, isIncoming, isTalking, isEnded),
                  ],
                ),
              ),
            ),
          ),
        );
      },
    );
  }

  Widget _buildActions(VoiceController voice, bool isIncoming, bool isTalking, bool isEnded) {
    if (isEnded) {
      return const SizedBox.shrink();
    }
    if (isIncoming) {
      return Row(
        mainAxisAlignment: MainAxisAlignment.spaceAround,
        children: [
          _CircleButton(
            color: const Color(0xFFEF4444),
            icon: Icons.call_end,
            onTap: voice.reject,
            label: '拒绝',
          ),
          _CircleButton(
            color: const Color(0xFF10B981),
            icon: Icons.call,
            onTap: voice.accept,
            label: '接听',
          ),
        ],
      );
    }
    // accepting 状态：只显示挂断按钮（还没建好通话不能切免提）
    if (!isTalking) {
      return Center(
        child: _CircleButton(
          color: const Color(0xFFEF4444),
          icon: Icons.call_end,
          onTap: voice.hangup,
          label: '挂断',
        ),
      );
    }
    // talking 状态：免提切换 + 挂断
    return Row(
      mainAxisAlignment: MainAxisAlignment.spaceAround,
      children: [
        _CircleButton(
          color: voice.speakerOn ? const Color(0xFFFBBF24) : const Color(0xFF475569),
          icon: voice.speakerOn ? Icons.volume_up : Icons.hearing,
          onTap: voice.toggleSpeaker,
          label: voice.speakerOn ? '免提' : '听筒',
        ),
        _CircleButton(
          color: const Color(0xFFEF4444),
          icon: Icons.call_end,
          onTap: voice.hangup,
          label: '挂断',
        ),
      ],
    );
  }
}

class _PulseIcon extends StatefulWidget {
  final bool animate;
  const _PulseIcon({required this.animate});
  @override
  State<_PulseIcon> createState() => _PulseIconState();
}

class _PulseIconState extends State<_PulseIcon> with SingleTickerProviderStateMixin {
  late final AnimationController _c;
  @override
  void initState() {
    super.initState();
    _c = AnimationController(vsync: this, duration: const Duration(milliseconds: 1400))
      ..repeat(reverse: true);
  }
  @override
  void dispose() { _c.dispose(); super.dispose(); }
  @override
  Widget build(BuildContext context) {
    return AnimatedBuilder(
      animation: _c,
      builder: (_, __) {
        final t = widget.animate ? _c.value : 0.0;
        return Container(
          width: 76, height: 76,
          decoration: BoxDecoration(
            shape: BoxShape.circle,
            color: Colors.white.withValues(alpha: 0.22),
            boxShadow: [
              BoxShadow(
                color: Colors.white.withValues(alpha: 0.35 * (1 - t)),
                blurRadius: 0,
                spreadRadius: 14 * t,
              ),
            ],
          ),
          child: const Icon(Icons.phone, color: Colors.white, size: 36),
        );
      },
    );
  }
}

class _CircleButton extends StatelessWidget {
  final Color color;
  final IconData icon;
  final VoidCallback onTap;
  final String label;
  const _CircleButton({
    required this.color, required this.icon, required this.onTap, required this.label,
  });
  @override
  Widget build(BuildContext context) {
    return Column(
      mainAxisSize: MainAxisSize.min,
      children: [
        InkResponse(
          onTap: onTap,
          radius: 36,
          child: Container(
            width: 60, height: 60,
            decoration: BoxDecoration(
              shape: BoxShape.circle,
              color: color,
              boxShadow: [
                BoxShadow(color: color.withValues(alpha: 0.45), blurRadius: 10, spreadRadius: 1),
              ],
            ),
            child: Icon(icon, color: Colors.white, size: 26),
          ),
        ),
        const SizedBox(height: 6),
        Text(label, style: const TextStyle(color: Colors.white70, fontSize: 12)),
      ],
    );
  }
}
