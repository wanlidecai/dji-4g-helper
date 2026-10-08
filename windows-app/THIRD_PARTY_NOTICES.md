# Third-Party Notices

大疆4g模块辅助工具 1.2.2 for Windows x64 includes a modified Windows port of DJOneHub / VoHive.

Required Notice: Copyright iniwex5 (https://github.com/iniwex5/vohive)

Upstream source: https://github.com/ZenGeekLabs/DJOneHub
Backend license: PolyForm Noncommercial License 1.0.0, retained in `LICENSE`.
Source used for this build: `windows-app/backend` in the project workspace (build scripts do not replace macOS source).

The Windows launcher and backend are compiled with Go 1.27.1 for Windows x64.
The Go runtime license and dependency licenses are retained under `licenses/`.

The package includes dynamically loaded libusb 1.0.30, LGPL 2.1 or later.

- Upstream: https://libusb.info/
- Source: https://github.com/libusb/libusb/releases/tag/v1.0.30
- Binary distribution: MSYS2 official UCRT64 package https://packages.msys2.org/packages/mingw-w64-ucrt-x86_64-libusb
- Package SHA256: `20106e6ff31b4581bb111b28a68b6b79a50dc3e5fc3033944e1c4b3ff8cd812e`
- License: `licenses/libusb-COPYING`
- The DLL is unmodified and replaceable in `runtime/libusb-1.0.dll`.

If supplied, the optional Zadig 2.9 driver installation utility is obtained from its official libwdi release. Driver installation is an explicit manual action in that utility.

- Official download: https://zadig.akeo.ie/
- Source: https://github.com/pbatard/libwdi/releases/tag/v1.5.1
- License: GNU GPL 3 or later, retained in `licenses/zadig-COPYING`.

This is a personal-use third-party tool. It is not affiliated with DJI, Quectel, or network operators.
