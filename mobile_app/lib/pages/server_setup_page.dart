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
  final _ctl = TextEditingController(text: 'http://');
  bool _testing = false;
  String? _hint;

  @override
  void dispose() {
    _ctl.dispose();
    super.dispose();
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
      // 先临时设置 baseUrl，再调 health
      await context.read<AppState>().setBackend(url);
      final h = await Api.health();
      if (h['status'] != 'ok') {
        setState(() => _hint = '服务器响应异常：${h.toString()}');
      } else {
        // 验证成功，AppState 已保存，会自动跳到登录页
      }
    } catch (e) {
      setState(() => _hint = '无法连接：$e\n请检查 URL 是否正确、服务器是否启动');
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
              const SizedBox(height: 40),
              TextField(
                controller: _ctl,
                keyboardType: TextInputType.url,
                autocorrect: false,
                decoration: const InputDecoration(
                  labelText: '服务器地址',
                  hintText: 'http://cs.example.com',
                  border: OutlineInputBorder(),
                  prefixIcon: Icon(Icons.link),
                ),
                onSubmitted: (_) => _confirm(),
              ),
              const SizedBox(height: 12),
              if (_hint != null)
                Container(
                  padding: const EdgeInsets.all(12),
                  decoration: BoxDecoration(
                    color: Colors.red.shade50,
                    borderRadius: BorderRadius.circular(8),
                    border: Border.all(color: Colors.red.shade200),
                  ),
                  child: Text(_hint!, style: TextStyle(color: Colors.red.shade800)),
                ),
              const SizedBox(height: 20),
              FilledButton(
                onPressed: _testing ? null : _confirm,
                child: _testing
                    ? const SizedBox(width: 18, height: 18, child: CircularProgressIndicator(strokeWidth: 2, color: Colors.white))
                    : const Text('连接'),
              ),
              const Spacer(),
              Text(
                '示例：http://38.76.193.68',
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
