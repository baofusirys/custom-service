import 'package:flutter/material.dart';
import 'package:intl/intl.dart';
import '../api/http_client.dart';

class AgentsPage extends StatefulWidget {
  const AgentsPage({super.key});

  @override
  State<AgentsPage> createState() => _AgentsPageState();
}

class _AgentsPageState extends State<AgentsPage> {
  bool _loading = true;
  List<Map<String, dynamic>> _list = [];

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    setState(() => _loading = true);
    try {
      _list = await Api.listAgents();
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('拉取失败：$e')));
      }
    }
    if (mounted) setState(() => _loading = false);
  }

  Future<void> _toggleActive(Map<String, dynamic> a) async {
    final active = a['active'] == true;
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('确认'),
        content: Text('确定${active ? '禁用' : '启用'}「${a['username']}」？'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('取消')),
          FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('确定')),
        ],
      ),
    );
    if (ok != true) return;
    try {
      final id = a['id'] is int ? a['id'] : int.tryParse(a['id'].toString()) ?? 0;
      await Api.setAgentActive(id, !active);
      _load();
    } catch (e) {
      if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('失败：$e')));
    }
  }

  Future<void> _showCreate() async {
    final username = TextEditingController();
    final password = TextEditingController();
    final nickname = TextEditingController();
    String role = 'agent';
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => StatefulBuilder(
        builder: (ctx, setSt) => AlertDialog(
          title: const Text('新建账号'),
          content: SingleChildScrollView(
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                TextField(controller: username, decoration: const InputDecoration(labelText: '用户名')),
                const SizedBox(height: 8),
                TextField(controller: password, obscureText: true, decoration: const InputDecoration(labelText: '密码（至少 8 位）')),
                const SizedBox(height: 8),
                TextField(controller: nickname, decoration: const InputDecoration(labelText: '昵称（可选）')),
                const SizedBox(height: 12),
                Row(
                  children: [
                    const Text('角色：'),
                    Radio<String>(value: 'agent', groupValue: role, onChanged: (v) => setSt(() => role = v ?? 'agent')),
                    const Text('客服'),
                    Radio<String>(value: 'admin', groupValue: role, onChanged: (v) => setSt(() => role = v ?? 'agent')),
                    const Text('管理员'),
                  ],
                ),
              ],
            ),
          ),
          actions: [
            TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('取消')),
            FilledButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('创建')),
          ],
        ),
      ),
    );
    if (ok != true) return;
    final u = username.text.trim();
    final p = password.text;
    if (u.isEmpty || p.length < 8) {
      if (mounted) ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('用户名必填，密码至少 8 位')));
      return;
    }
    try {
      await Api.createAgent(username: u, password: p, role: role, nickname: nickname.text.trim());
      _load();
    } catch (e) {
      if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('失败：$e')));
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text('客服管理'),
        actions: [
          IconButton(icon: const Icon(Icons.add), onPressed: _showCreate, tooltip: '新建账号'),
          IconButton(icon: const Icon(Icons.refresh), onPressed: _load),
        ],
      ),
      body: _loading
          ? const Center(child: CircularProgressIndicator())
          : ListView.separated(
              itemCount: _list.length,
              separatorBuilder: (_, __) => const Divider(height: 1, indent: 16),
              itemBuilder: (ctx, i) {
                final a = _list[i];
                final active = a['active'] == true;
                final role = a['role']?.toString() ?? 'agent';
                final username = a['username']?.toString() ?? '';
                final nickname = a['nickname']?.toString() ?? '';
                final lastLogin = a['last_login'];
                return ListTile(
                  leading: CircleAvatar(
                    backgroundColor: role == 'admin' ? Colors.red.shade400 : const Color(0xFF2974FF),
                    child: Text(
                      username.isNotEmpty ? username[0].toUpperCase() : '?',
                      style: const TextStyle(color: Colors.white),
                    ),
                  ),
                  title: Row(
                    children: [
                      Text(username, style: const TextStyle(fontWeight: FontWeight.w600)),
                      const SizedBox(width: 6),
                      Container(
                        padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 1),
                        decoration: BoxDecoration(
                          color: role == 'admin' ? Colors.red.shade50 : Colors.blue.shade50,
                          borderRadius: BorderRadius.circular(4),
                        ),
                        child: Text(
                          role == 'admin' ? '管理员' : '客服',
                          style: TextStyle(fontSize: 11, color: role == 'admin' ? Colors.red.shade700 : Colors.blue.shade700),
                        ),
                      ),
                      if (!active) ...[
                        const SizedBox(width: 6),
                        const Text('（已禁用）', style: TextStyle(fontSize: 11, color: Colors.grey)),
                      ],
                    ],
                  ),
                  subtitle: Text(
                    nickname.isNotEmpty
                      ? '$nickname · 最近登录 ${_fmtLogin(lastLogin)}'
                      : '最近登录 ${_fmtLogin(lastLogin)}',
                    style: const TextStyle(fontSize: 12),
                  ),
                  trailing: TextButton(
                    onPressed: () => _toggleActive(a),
                    child: Text(active ? '禁用' : '启用',
                        style: TextStyle(color: active ? Colors.red : Colors.green)),
                  ),
                );
              },
            ),
    );
  }

  String _fmtLogin(dynamic v) {
    if (v == null) return '从未';
    try {
      final t = DateTime.parse(v.toString().replaceFirst(' ', 'T')).toLocal();
      return DateFormat('yyyy-MM-dd HH:mm').format(t);
    } catch (_) {
      return '未知';
    }
  }
}
