# mobile_app — Flutter 客服工作台（iOS + Android）

## 一句话
Web 客服工作台的双端原生 App 复刻。访客发消息直接推送到客服 iPhone / Android，客服在手机上就能回复，跟在 Web 端体验一致。

## 调用关系
- **被调用**：客服打开 App
- **调用**：用户配置的后端服务（HTTP `/api/agent/*` + WSS `/ws/agent`）

## 第一次初始化（爷爷需要做的）

### Windows 上（验证 Android 版本）
1. 装 [Flutter SDK](https://docs.flutter.dev/get-started/install/windows)（>= 3.13）+ Android Studio + Android SDK
2. 在 `mobile_app/` 目录下执行：
   ```powershell
   flutter create -t app --org com.customservice --platforms=ios,android .
   flutter pub get
   flutter run    # 接上 Android 真机或启动模拟器
   ```
3. 第一次进入 App 会要求输入服务器 URL（如 `https://cs.yourcompany.com`），然后用 `.env` 里 `ADMIN_BOOTSTRAP_USERNAME` / `ADMIN_BOOTSTRAP_PASSWORD` 配的超管账号登录

### macOS 上（编译 iOS）
1. 装 Xcode 15+ + Flutter SDK
2. 同样在 `mobile_app/` 执行 `flutter create ... .` 和 `flutter pub get`
3. `cd ios && pod install && cd ..`
4. `flutter run -d <iPhone真机ID>`（模拟器收不到 APNs，要测推送必须真机）

### iOS 关键配置
首次运行后需要在 `ios/Runner/Info.plist` 加这段（**允许 HTTP** 才能连测试服）：
```xml
<key>NSAppTransportSecurity</key>
<dict>
  <key>NSAllowsArbitraryLoads</key>
  <true/>
</dict>
```
生产部署用 HTTPS 后这段可以去掉。

## 关键开关 / 配置
| 项 | 文件 | 位置 |
| --- | --- | --- |
| 当前后端 URL | SharedPreferences key `backend_url` | 由 `ServerSetupPage` 写入 |
| 当前登录 token | SharedPreferences key `agent_token` | 由 `LoginPage` 登录后写入 |
| WSS 心跳 / 重连 | `lib/api/ws_client.dart` | 文件顶部常量 |
| 主题色 | `lib/app.dart` | `seedColor` |

## 已知坑
- iOS 模拟器**收不到** APNs（Apple 规定，必须真机）
- 切换服务器后会自动登出（token 跟着服务器走）

## 上次重大改动
- 2026-05-23 [020] 第 1 批：项目骨架 + URL 配置 + 登录 + 会话列表 + 聊天页 + WSS 实时
- 待办 [021]：历史 / 客服管理 / 系统设置
- 待办 [022]：APNs / FCM 推送集成
