import 'package:flutter/material.dart';
import 'package:provider/provider.dart';
import '../api/http_client.dart';
import '../state/app_state.dart';

/// 首次启动配置后端 URL（自托管友好）。
/// 输入完会调 /api/health 验证可达性，再保存。
class ServerSetupPage extends StatefulWidget {
  const ServerSetupPage({super.key});

  @override
  State<ServerSetupPage> createState() => _ServerSetupPageState();
}

class _ServerSetupPageState extends State<ServerSetupPage> {
  final _ctl = TextEditingController();
  bool _testing = false;
  String? _hint;

  // 快速填入测试服（爷爷的服务器，maihaocs.icu 已挂 HTTPS）
  static const _demoUrl = 'https://maihaocs.icu';

  @override
  void dispose() {
    _ctl.dispose();
    super.dispose();
  }

  Widget _hintRow(String tag, String example) {
    return Padding(
      padding: const EdgeInsets.only(top: 4),
      child: Row(
        children: [
          Container(
            padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 1),
            decoration: BoxDecoration(
              color: const Color(0xFFDBEAFE),
              borderRadius: BorderRadius.circular(4),
            ),
            child: Text(
              tag,
              style: const TextStyle(fontSize: 11, color: Color(0xFF1E3A8A), fontWeight: FontWeight.w500),
            ),
          ),
          const SizedBox(width: 8),
          Expanded(
            child: SelectableText(
              example,
              style: const TextStyle(
                fontFamily: 'monospace',
                fontSize: 12,
                color: Color(0xFF1F2937),
              ),
            ),
          ),
        ],
      ),
    );
  }

  Future<void> _confirm() async {
    final url = _ctl.text.trim().replaceAll(RegExp(r'/+$'), '');
    if (!url.startsWith('http://') && !url.startsWith('https://')) {
      setState(() => _hint = '请输入完整 URL，含 http:// 或 https://');
      return;
    }
    setState(() {
      _testing = true;
      _hint = null;
    });
    try {
      // 先用临时 baseUrl 测 health，不动 AppState；测通了才 setBackend
      // 否则 setBackend 会立刻 notifyListeners 让本页 dispose，后续 await 报
      // setState-after-dispose
      final h = await Api.healthAt(url);
      if (h['status'] != 'ok') {
        if (mounted) setState(() => _hint = '服务器响应异常：${h.toString()}');
        return;
      }
      // 测通了再保存 — 这一刻顶层会切到登录页，本页随即 dispose
      if (!mounted) return;
      await context.read<AppState>().setBackend(url);
    } catch (e) {
      if (mounted) setState(() => _hint = '无法连接：$e\n请检查 URL 是否正确、服务器是否启动');
    } finally {
      if (mounted) setState(() => _testing = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      body: SafeArea(
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 28, vertical: 32),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              const SizedBox(height: 48),
              Container(
                width: 72,
                height: 72,
                decoration: BoxDecoration(
                  borderRadius: BorderRadius.circular(16),
                  gradient: const LinearGradient(
                    colors: [Color(0xFF4A90FF), Color(0xFF2974FF)],
                  ),
                ),
                child: const Icon(Icons.headset_mic, color: Colors.white, size: 36),
              ),
              const SizedBox(height: 24),
              Text('客服工作台', style: Theme.of(context).textTheme.headlineMedium),
              const SizedBox(height: 8),
              Text(
                '请输入您部署的客服服务器地址',
                style: Theme.of(context).textTheme.bodyMedium?.copyWith(color: Colors.grey[600]),
              ),
              const SizedBox(height: 32),
              TextField(
                controller: _ctl,
                keyboardType: TextInputType.url,
                autocorrect: false,
                decoration: const InputDecoration(
                  labelText: '服务器地址',
                  hintText: '例如：https://maihaocs.icu',
                  helperText: '必须以 http:// 或 https:// 开头',
                  border: OutlineInputBorder(),
                  prefixIcon: Icon(Icons.link),
                ),
                onSubmitted: (_) => _confirm(),
              ),
              const SizedBox(height: 12),
              // 格式说明卡片
              Container(
                padding: const EdgeInsets.all(14),
                decoration: BoxDecoration(
                  color: const Color(0xFFEFF6FF),
                  borderRadius: BorderRadius.circular(8),
                  border: Border.all(color: const Color(0xFFBFDBFE)),
                ),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Row(
                      children: const [
                        Icon(Icons.info_outline, size: 16, color: Color(0xFF2974FF)),
                        SizedBox(width: 6),
                        Text(
                          '怎么填？',
                          style: TextStyle(fontSize: 13, fontWeight: FontWeight.w600, color: Color(0xFF1E3A8A)),
                        ),
                      ],
                    ),
                    const SizedBox(height: 8),
                    _hintRow('域名 HTTPS', 'https://maihaocs.icu'),
                    _hintRow('IP 地址', 'http://38.76.193.68'),
                    _hintRow('带端口', 'http://192.168.1.100:8080'),
                    const SizedBox(height: 6),
                    Text(
                      '不要在末尾加 /；不要加 /admin 或 /api',
                      style: TextStyle(fontSize: 11, color: Colors.grey[700], fontStyle: FontStyle.italic),
                    ),
                  ],
                ),
              ),
              const SizedBox(height: 12),
              // 一键填入测试服
              OutlinedButton.icon(
                icon: const Icon(Icons.flash_on, size: 16),
                label: const Text('一键填入测试服 maihaocs.icu'),
                onPressed: () {
                  _ctl.text = _demoUrl;
                  _ctl.selection = TextSelection.fromPosition(TextPosition(offset: _ctl.text.length));
                  setState(() => _hint = null);
                },
              ),
              if (_hint != null) ...[
                const SizedBox(height: 12),
                Container(
                  padding: const EdgeInsets.all(12),
                  decoration: BoxDecoration(
                    color: Colors.red.shade50,
                    borderRadius: BorderRadius.circular(8),
                    border: Border.all(color: Colors.red.shade200),
                  ),
                  child: Text(_hint!, style: TextStyle(color: Colors.red.shade800)),
                ),
              ],
              const SizedBox(height: 20),
              FilledButton(
                onPressed: _testing ? null : _confirm,
                style: FilledButton.styleFrom(
                  padding: const EdgeInsets.symmetric(vertical: 14),
                ),
                child: _testing
                    ? const SizedBox(width: 18, height: 18, child: CircularProgressIndicator(strokeWidth: 2, color: Colors.white))
                    : const Text('连 接', style: TextStyle(fontSize: 16)),
              ),
              const Spacer(),
              Text(
                'App 会自动调 /api/health 验证服务器可达后才保存',
                style: Theme.of(context).textTheme.bodySmall?.copyWith(color: Colors.grey),
                textAlign: TextAlign.center,
              ),
            ],
          ),
        ),
      ),
    );
  }
}
