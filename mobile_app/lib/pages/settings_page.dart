import 'package:flutter/material.dart';
import '../api/http_client.dart';
import '../api/sound.dart';

class SettingsPage extends StatefulWidget {
  const SettingsPage({super.key});

  @override
  State<SettingsPage> createState() => _SettingsPageState();
}

class _SettingsPageState extends State<SettingsPage> {
  bool _loading = true;
  bool _saving = false;

  String _agentSound = 'agent1';
  String _visitorSound = 'visitor1';
  bool _notifyVisitorEnter = true;
  bool _greetingEnabled = true;
  final _greetingText = TextEditingController(
      text: '您好，欢迎光临！请问有什么可以帮您？');
  final _widgetTitle = TextEditingController(text: '在线客服');

  final _soundOptions = listSounds();

  @override
  void initState() {
    super.initState();
    _load();
  }

  @override
  void dispose() {
    _greetingText.dispose();
    _widgetTitle.dispose();
    super.dispose();
  }

  Future<void> _load() async {
    setState(() => _loading = true);
    try {
      final s = await Api.getSettings();
      _agentSound = (s['agent_notify_sound'] ?? 'agent1').toString();
      _visitorSound = (s['visitor_notify_sound'] ?? 'visitor1').toString();
      _notifyVisitorEnter = (s['notify_visitor_enter']?.toString() ?? 'true') != 'false';
      _greetingEnabled = (s['greeting_enabled']?.toString() ?? 'true') != 'false';
      if (s['greeting_text'] != null && s['greeting_text'].toString().isNotEmpty) {
        _greetingText.text = s['greeting_text'].toString();
      }
      if (s['widget_title'] != null && s['widget_title'].toString().isNotEmpty) {
        _widgetTitle.text = s['widget_title'].toString();
      }
    } catch (e) {
      if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('拉取设置失败：$e')));
    }
    if (mounted) setState(() => _loading = false);
  }

  Future<void> _save() async {
    setState(() => _saving = true);
    try {
      await Api.saveSettings({
        'agent_notify_sound': _agentSound,
        'visitor_notify_sound': _visitorSound,
        'notify_visitor_enter': _notifyVisitorEnter ? 'true' : 'false',
        'greeting_enabled': _greetingEnabled ? 'true' : 'false',
        'greeting_text': _greetingText.text,
        'widget_title': _widgetTitle.text,
      });
      if (mounted) ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('保存成功')));
    } catch (e) {
      if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text('保存失败：$e')));
    }
    if (mounted) setState(() => _saving = false);
  }

  @override
  Widget build(BuildContext context) {
    if (_loading) {
      return Scaffold(
        appBar: AppBar(title: const Text('系统设置')),
        body: const Center(child: CircularProgressIndicator()),
      );
    }
    return Scaffold(
      appBar: AppBar(title: const Text('系统设置')),
      // 底部固定「保存」按钮（避免藏在 AppBar 右上角看不见）
      bottomNavigationBar: SafeArea(
        child: Container(
          padding: const EdgeInsets.fromLTRB(16, 8, 16, 8),
          decoration: const BoxDecoration(
            color: Colors.white,
            border: Border(top: BorderSide(color: Color(0xFFE5E7EB))),
          ),
          child: FilledButton(
            onPressed: _saving ? null : _save,
            style: FilledButton.styleFrom(
              minimumSize: const Size.fromHeight(48),
              textStyle: const TextStyle(fontSize: 16, fontWeight: FontWeight.w600),
            ),
            child: _saving
                ? const SizedBox(
                    width: 20, height: 20,
                    child: CircularProgressIndicator(strokeWidth: 2, color: Colors.white),
                  )
                : const Text('保 存'),
          ),
        ),
      ),
      body: ListView(
        children: [
          _section('通知声音'),
          _soundTile(
            title: '客服端提示音',
            hint: '客服收到访客消息时播放',
            value: _agentSound,
            onChanged: (v) => setState(() => _agentSound = v),
          ),
          _soundTile(
            title: '访客端提示音',
            hint: '访客的 widget 收到客服消息时播放',
            value: _visitorSound,
            onChanged: (v) => setState(() => _visitorSound = v),
          ),
          _section('访客进入网站'),
          SwitchListTile(
            value: _notifyVisitorEnter,
            onChanged: (v) => setState(() => _notifyVisitorEnter = v),
            title: const Text('通知客服'),
            subtitle: const Text('访客打开页面时客服端弹通知 + 响声'),
          ),
          SwitchListTile(
            value: _greetingEnabled,
            onChanged: (v) => setState(() => _greetingEnabled = v),
            title: const Text('自动问候'),
            subtitle: const Text('新访客进入时自动发送一条问候消息'),
          ),
          if (_greetingEnabled)
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 4, 16, 16),
              child: TextField(
                controller: _greetingText,
                maxLines: 3,
                maxLength: 500,
                decoration: const InputDecoration(
                  labelText: '问候内容',
                  border: OutlineInputBorder(),
                ),
              ),
            ),
          _section('显示'),
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 4, 16, 24),
            child: TextField(
              controller: _widgetTitle,
              maxLength: 50,
              decoration: const InputDecoration(
                labelText: 'Widget 标题',
                helperText: '访客端聊天窗口顶部显示',
                border: OutlineInputBorder(),
              ),
            ),
          ),
          const SizedBox(height: 24),
        ],
      ),
    );
  }

  Widget _section(String title) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(20, 20, 16, 8),
      child: Text(title, style: const TextStyle(fontSize: 12, color: Colors.grey, fontWeight: FontWeight.w600)),
    );
  }

  Widget _soundTile({
    required String title,
    required String hint,
    required String value,
    required ValueChanged<String> onChanged,
  }) {
    return Container(
      color: Colors.white,
      padding: const EdgeInsets.fromLTRB(16, 12, 16, 12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(title, style: const TextStyle(fontWeight: FontWeight.w600)),
          const SizedBox(height: 4),
          Text(hint, style: TextStyle(fontSize: 12, color: Colors.grey[600])),
          const SizedBox(height: 8),
          Row(
            children: [
              Expanded(
                child: DropdownButtonFormField<String>(
                  initialValue: value,
                  isDense: true,
                  decoration: const InputDecoration(
                    border: OutlineInputBorder(),
                    contentPadding: EdgeInsets.symmetric(horizontal: 12, vertical: 8),
                  ),
                  items: _soundOptions.map((o) =>
                    DropdownMenuItem<String>(value: o['value'], child: Text(o['label']!))
                  ).toList(),
                  onChanged: (v) {
                    if (v != null) onChanged(v);
                  },
                ),
              ),
              const SizedBox(width: 8),
              TextButton.icon(
                icon: const Icon(Icons.play_arrow, size: 18),
                label: const Text('试听'),
                onPressed: () => playSound(value),
              ),
            ],
          ),
        ],
      ),
    );
  }
}
