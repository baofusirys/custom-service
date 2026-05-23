import 'package:flutter/material.dart';
import 'package:provider/provider.dart';
import '../state/app_state.dart';
import 'history_page.dart';
import 'agents_page.dart';
import 'settings_page.dart';

class MePage extends StatelessWidget {
  const MePage({super.key});

  @override
  Widget build(BuildContext context) {
    final state = context.watch<AppState>();
    final agent = state.agent;
    return Scaffold(
      appBar: AppBar(title: const Text('我的')),
      body: ListView(
        children: [
          const SizedBox(height: 12),
          Container(
            margin: const EdgeInsets.symmetric(horizontal: 16),
            padding: const EdgeInsets.all(20),
            decoration: BoxDecoration(
              gradient: const LinearGradient(
                colors: [Color(0xFF4A90FF), Color(0xFF2974FF)],
              ),
              borderRadius: BorderRadius.circular(12),
            ),
            child: Row(
              children: [
                CircleAvatar(
                  radius: 28,
                  backgroundColor: Colors.white24,
                  child: Text(
                    (agent?.nickname.isNotEmpty == true ? agent!.nickname : (agent?.username ?? '?'))[0].toUpperCase(),
                    style: const TextStyle(color: Colors.white, fontSize: 22, fontWeight: FontWeight.w600),
                  ),
                ),
                const SizedBox(width: 16),
                Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      agent?.nickname.isNotEmpty == true ? agent!.nickname : (agent?.username ?? ''),
                      style: const TextStyle(color: Colors.white, fontSize: 18, fontWeight: FontWeight.w600),
                    ),
                    const SizedBox(height: 4),
                    Text(
                      agent?.role == 'admin' ? '管理员' : '客服',
                      style: const TextStyle(color: Colors.white70, fontSize: 13),
                    ),
                  ],
                ),
              ],
            ),
          ),
          const SizedBox(height: 16),
          _section('服务器'),
          _tile(
            icon: Icons.link,
            title: '后端地址',
            subtitle: state.backendUrl ?? '',
            onTap: () => _confirmSwitch(context),
          ),
          _tile(
            icon: Icons.health_and_safety_outlined,
            title: 'WSS 连接状态',
            subtitle: state.wsAlive ? '已连接' : '已断开',
            trailing: Container(
              width: 10,
              height: 10,
              decoration: BoxDecoration(
                shape: BoxShape.circle,
                color: state.wsAlive ? Colors.green : Colors.red,
              ),
            ),
          ),
          _section('管理'),
          _tile(
            icon: Icons.history,
            title: '历史记录',
            onTap: () => Navigator.of(context).push(MaterialPageRoute(builder: (_) => const HistoryPage())),
          ),
          if (agent?.role == 'admin') ...[
            _tile(
              icon: Icons.group,
              title: '客服管理',
              onTap: () => Navigator.of(context).push(MaterialPageRoute(builder: (_) => const AgentsPage())),
            ),
            _tile(
              icon: Icons.settings,
              title: '系统设置',
              onTap: () => Navigator.of(context).push(MaterialPageRoute(builder: (_) => const SettingsPage())),
            ),
          ],
          _section('待开发（第 3 批 [025]）'),
          _tile(icon: Icons.notifications_active, title: 'APNs / FCM 推送', enabled: false),
          const SizedBox(height: 24),
          Container(
            margin: const EdgeInsets.symmetric(horizontal: 16),
            child: OutlinedButton.icon(
              icon: const Icon(Icons.logout, color: Colors.red),
              label: const Text('退出登录', style: TextStyle(color: Colors.red)),
              style: OutlinedButton.styleFrom(
                side: const BorderSide(color: Colors.red),
                padding: const EdgeInsets.symmetric(vertical: 14),
              ),
              onPressed: () => _confirmLogout(context),
            ),
          ),
          const SizedBox(height: 32),
        ],
      ),
    );
  }

  Widget _section(String title) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(20, 16, 16, 8),
      child: Text(title, style: const TextStyle(fontSize: 12, color: Colors.grey, fontWeight: FontWeight.w600)),
    );
  }

  Widget _tile({
    required IconData icon,
    required String title,
    String? subtitle,
    Widget? trailing,
    VoidCallback? onTap,
    bool enabled = true,
  }) {
    return Container(
      color: Colors.white,
      child: ListTile(
        leading: Icon(icon, color: enabled ? const Color(0xFF2974FF) : Colors.grey),
        title: Text(title, style: TextStyle(color: enabled ? null : Colors.grey)),
        subtitle: subtitle != null ? Text(subtitle, maxLines: 1, overflow: TextOverflow.ellipsis) : null,
        trailing: trailing ?? (enabled ? const Icon(Icons.chevron_right) : null),
        onTap: enabled ? onTap : null,
      ),
    );
  }

  void _confirmSwitch(BuildContext context) {
    showDialog(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('切换服务器'),
        content: const Text('切换后会自动退出当前账号。确定要切换吗？'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx), child: const Text('取消')),
          FilledButton(
            onPressed: () {
              Navigator.pop(ctx);
              context.read<AppState>().setBackend('');
            },
            child: const Text('切换'),
          ),
        ],
      ),
    );
  }

  void _confirmLogout(BuildContext context) {
    showDialog(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('退出登录'),
        content: const Text('确定要退出吗？'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx), child: const Text('取消')),
          FilledButton(
            onPressed: () {
              Navigator.pop(ctx);
              context.read<AppState>().logout();
            },
            child: const Text('退出'),
          ),
        ],
      ),
    );
  }
}
