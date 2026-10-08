# 大疆4g模块辅助工具 · 宣传动画

48 秒产品动画，横屏 1920×1080 与竖屏 1080×1920，30 fps。
包含中文合成女声、原创合成音乐与画面字幕；所有界面和数据均为示意。

[下载成片及完整素材包](https://github.com/wanlidecai/dji-4g-helper/releases/tag/v1.2.2)

本目录保留分镜、配音稿、字幕、品牌图标和可编辑的渲染源码。
制作脚本使用 macOS 中文字体，在 Apple 芯片 Mac 上制作。

从仓库根目录执行：

```sh
python3 -m venv .venv
. .venv/bin/activate
python3 -m pip install -r promo/requirements.txt
python3 promo/dji-4g-helper-1.2.2/audio/generate_audio.py
python3 promo/dji-4g-helper-1.2.2/render_motion.py --format landscape
python3 promo/dji-4g-helper-1.2.2/render_motion.py --format landscape --mux
python3 promo/dji-4g-helper-1.2.2/render_motion.py --format portrait
python3 promo/dji-4g-helper-1.2.2/render_motion.py --format portrait --mux
```

配音默认使用 `zh-CN-XiaoxiaoNeural`；生成语音需要联网。若在线服务失败，
脚本在 macOS 上尝试使用系统 `Tingting` 语音，并记录所使用的引擎。
切换男声可传 `--voice zh-CN-YunxiNeural`。音乐直接由 NumPy 合成，无外部采样。

完成后打开 `promo/dji-4g-helper-1.2.2/播放宣传动画.html`。
完整素材包内的播放器与视频可直接配合使用。若从发行页单独下载 MP4，
放入该目录后，需将横屏文件重命名为 `大疆4g模块辅助工具-宣传动画-横屏-配音版.mp4`，
竖屏文件重命名为 `大疆4g模块辅助工具-宣传动画-竖屏-配音版.mp4`，再打开此播放器。

动画中的功能说明以 1.2.2 为依据：电脑双向通话音频尚未实现，
Windows 兼容性待实机验证，本项目是独立第三方工具。
