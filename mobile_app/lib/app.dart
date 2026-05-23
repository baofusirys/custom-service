import 'package:flutter/material.dart';
import 'package:provider/provider.dart';
import 'state/app_state.dart';
import 'pages/server_setup_page.dart';
import 'pages/login_page.dart';
import 'pages/home_page.dart';

/// 顶层路由：根据 AppState 决定显示哪个根页面
///   - 没有 backendUrl   → 服务器配置页
///   - 没有 token        → 登录页
///   - 都有              → 主页（底部 Tab）
class CustomServiceApp extends StatelessWidget {
  const CustomServiceApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: '客服工作台',
      debugShowCheckedModeBanner: false,
      theme: ThemeData(
        useMaterial3: true,
        colorScheme: ColorScheme.fromSeed(
          seedColor: const Color(0xFF2974FF),
          brightness: Brightness.light,
        ),
        scaffoldBackgroundColor: const Color(0xFFF7F8FA),
      ),
      home: const _Root(),
    );
  }
}

class _Root extends StatelessWidget {
  const _Root();

  @override
  Widget build(BuildContext context) {
    return Consumer<AppState>(
      builder: (ctx, state, _) {
        if (state.backendUrl == null || state.backendUrl!.isEmpty) {
          return const ServerSetupPage();
        }
        if (state.token == null || state.token!.isEmpty || state.agent == null) {
          return const LoginPage();
        }
        return const HomePage();
      },
    );
  }
}
