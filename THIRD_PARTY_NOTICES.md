# Third-Party Notices / 第三方组件声明

“碗里的菜”包含基于 [DJOneHub](https://github.com/ZenGeekLabs/DJOneHub) 和 [VoHive](https://github.com/iniwex5/vohive) 演进的代码，以及本仓库的桌面启动器、通知、唤醒恢复和界面改动。

后端源代码位于 `windows-app/backend/`，同时供 Mac 与 Windows 构建使用。原后端许可证和来源声明完整保留于：

- [`LICENSE`](LICENSE)
- [`windows-app/backend/LICENSE`](windows-app/backend/LICENSE)
- [`windows-app/backend/THIRD_PARTY_NOTICES.md`](windows-app/backend/THIRD_PARTY_NOTICES.md)

项目沿用 **PolyForm Noncommercial License 1.0.0**，不会通过桌面包装或重新命名改变上游许可。

必须保留的上游声明：

```text
Required Notice: Copyright iniwex5 (https://github.com/iniwex5/vohive)
```

## libusb

发行包包含动态加载的 libusb。v1.2.1 发布包使用 **libusb 1.0.30**，许可为 **GNU Lesser General Public License 2.1 或更新版本**。

- 项目主页：<https://libusb.info/>
- 该版本源码：<https://github.com/libusb/libusb/releases/tag/v1.0.30>
- 原始许可文本：[`windows-app/libusb-COPYING`](windows-app/libusb-COPYING)
- Mac 发行包库路径：`碗里的菜.app/Contents/Resources/runtime/lib/libusb-1.0.0.dylib`
- Windows 发行包库路径：`runtime/libusb-1.0.dll`
- 发行包同时保留 `licenses/libusb-COPYING`。

Windows DLL 来自官方 MSYS2 UCRT64 二进制分发，原文件未修改，可替换。来源、包校验值及 Windows 打包说明见 [`windows-app/THIRD_PARTY_NOTICES.md`](windows-app/THIRD_PARTY_NOTICES.md)。从源码本地构建时，也可使用开发者安装的兼容 libusb；请保留所用版本的许可和来源。

## Go 运行时与依赖

Go 运行时与各 Go 模块遵循各自许可证。Windows 打包脚本会将 Go 运行时许可及所使用模块的可用 LICENSE / COPYING / NOTICE 文件复制到发行包 `licenses/`。源码中的 vendored 依赖保留在 `windows-app/backend/third_party/`，原始署名与许可文本不作替换。

| 源码组件 | 原始许可文件 |
| --- | --- |
| euicc-go | [`windows-app/backend/third_party/euicc-go/LICENSE`](windows-app/backend/third_party/euicc-go/LICENSE) |
| uicc-go | [`windows-app/backend/third_party/uicc-go/LICENSE`](windows-app/backend/third_party/uicc-go/LICENSE) |
| quectel-qmi-go | [`windows-app/backend/third_party/quectel-qmi-go/LICENSE`](windows-app/backend/third_party/quectel-qmi-go/LICENSE) |
| strftime | [`windows-app/backend/third_party/strftime/LICENSE`](windows-app/backend/third_party/strftime/LICENSE) |
| pkg/errors | [`windows-app/backend/third_party/pkg-errors/LICENSE`](windows-app/backend/third_party/pkg-errors/LICENSE) |
| golang.org/x/sys | [`windows-app/backend/third_party/x-sys/LICENSE`](windows-app/backend/third_party/x-sys/LICENSE) |
| golang.org/x/text | [`windows-app/backend/third_party/x-text/LICENSE`](windows-app/backend/third_party/x-text/LICENSE) |
| multierr | [`windows-app/backend/third_party/multierr/LICENSE.txt`](windows-app/backend/third_party/multierr/LICENSE.txt) |

其他依赖及版本列在 [`windows-app/backend/go.mod`](windows-app/backend/go.mod) 和 [`windows-app/go.mod`](windows-app/go.mod)，其版权归各自作者所有。

## 可选 Zadig 驱动工具

Windows 打包脚本支持携带 **Zadig 2.9**；若未提供工具，则只附官网下载快捷方式。Zadig 遵循 **GNU GPL 3 或更新版本**，仅由用户手动运行以安装驱动。

- 官方下载：<https://zadig.akeo.ie/>
- 对应 libwdi 源码：<https://github.com/pbatard/libwdi/releases/tag/v1.5.1>
- 原始许可文本：[`windows-app/zadig-COPYING`](windows-app/zadig-COPYING)
- 携带 Zadig 的发行包保留 `licenses/zadig-COPYING`。

本声明供查阅，不替代任何组件的完整许可证。DJI、大疆、Quectel 等商标和产品名称归各自权利人所有；本项目不代表这些公司或任何运营商。
