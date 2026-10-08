# DJOneHubNotifier

DJOneHub 的 macOS 原生通知助手。网页关闭后仍可显示来电和短信：

- 每秒读取来电状态，显示 iOS 风格悬浮卡片。
- “拒接”调用 DJOneHub 挂断接口；“详情”打开管理页面。
- 每三秒读取短信列表，只提醒启动后新收到的短信。
- 不直接访问 USB，不改变短信模式、上网模式或网络切换规则。

## 构建

```bash
./build-app.sh
```

构建脚本会使用项目内缓存、执行内置自检、生成临时签名的 App，并验证签名和 `Info.plist`。

输出位置：

```text
dist/DJOneHubNotifier.app
```

## 验证

```bash
dist/DJOneHubNotifier.app/Contents/MacOS/DJOneHubNotifier --health-check
dist/DJOneHubNotifier.app/Contents/MacOS/DJOneHubNotifier --preview call
dist/DJOneHubNotifier.app/Contents/MacOS/DJOneHubNotifier --preview sms
```

`--health-check` 只输出接口解析状态和条数，不输出号码或短信内容。

## 常驻运行

默认安装位置：

```text
~/Library/Application Support/DJOneHub/notifier/DJOneHubNotifier.app
```

先将 `com.jamie.djonehub-notifier.plist` 中的 `YOUR_USERNAME` 替换为当前 macOS 用户名，再复制到 `~/Library/LaunchAgents/`，通过 `launchctl bootstrap` 注册。LaunchAgent 不会自动展开 `~`。助手要求 DJOneHub 继续监听 `http://127.0.0.1:7575/`。
