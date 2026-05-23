import 'package:flutter/material.dart';
import 'package:provider/provider.dart';
import '../state/app_state.dart';

class LoginPage extends StatefulWidget {
  const LoginPage({super.key});

  @override
  State<LoginPage> createState() => _LoginPageState();
}

class _LoginPageState extends State<LoginPage> {
  final _u = TextEditingController();
  final _p = TextEditingController();
  bool _loading = false;
  String? _err;

  @override
  void dispose() {
    _u.dispose();
    _p.dispose();
    super.dispose();
  }

  Future<void> _submit() async {
    if (_u.text.trim().isEmpty || _p.text.trim().isEmpty) return;
    setState(() {
      _loading = true;
      _err = null;
    });
    final err = await context.read<AppState>().login(_u.text.trim(), _p.text);
    if (mounted) {
      setState(() {
        _loading = false;
        _err = err;
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    final backendUrl = context.watch<AppState>().backendUrl ?? '';
    return Scaffold(
      body: SafeArea(
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 28),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              const SizedBox(height: 64),
              Text('登 录', style: Theme.of(context).textTheme.headlineMedium),
              const SizedBox(height: 8),
              Text(
                backendUrl,
                style: Theme.of(context).textTheme.bodySmall?.copyWith(color: Colors.grey),
              ),
              const SizedBox(height: 32),
              TextField(
                controller: _u,
                decoration: const InputDecoration(
                  labelText: '账号',
                  border: OutlineInputBorder(),
                  prefixIcon: Icon(Icons.person_outline),
                ),
              ),
              const SizedBox(height: 12),
              TextField(
                controller: _p,
                obscureText: true,
                decoration: const InputDecoration(
                  labelText: '密码',
                  border: OutlineInputBorder(),
                  prefixIcon: Icon(Icons.lock_outline),
                ),
                onSubmitted: (_) => _submit(),
              ),
              const SizedBox(height: 16),
              if (_err != null)
                Container(
                  padding: const EdgeInsets.all(12),
                  decoration: BoxDecoration(
                    color: Colors.red.shade50,
                    borderRadius: BorderRadius.circular(8),
                  ),
                  child: Text(_err!, style: TextStyle(color: Colors.red.shade800)),
                ),
              const SizedBox(height: 20),
              FilledButton(
                onPressed: _loading ? null : _submit,
                child: _loading
                    ? const SizedBox(width: 18, height: 18, child: CircularProgressIndicator(strokeWidth: 2, color: Colors.white))
                    : const Text('登 录'),
              ),
              const SizedBox(height: 12),
              TextButton(
                onPressed: _loading ? null : () => context.read<AppState>().setBackend(''),
                child: const Text('切换服务器地址'),
              ),
            ],
          ),
        ),
      ),
    );
  }
}
