# Tauri 客户端目录

跨平台桌面客户端，通过 sidecar 嵌入 Go 后端二进制。

## 构建

```bash
# 前置：编译 Go 二进制到 binaries/，并按平台添加后缀
#   binaries/managi-x86_64-pc-windows-msvc.exe
#   binaries/managi-x86_64-apple-darwin
#   binaries/managi-x86_64-unknown-linux-gnu

cargo tauri build
```

设计见 ../../design-v3.md 第七章。
