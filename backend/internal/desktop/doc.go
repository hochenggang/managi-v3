// Package desktop - Windows 桌面托盘形态：同一个后端，双击即用。
//
// 内嵌前端单页与托盘图标、只监听 127.0.0.1、启动后经默认浏览器打开首页，
// 端口被占用时自动顺延并把失败原因显示在托盘上（而不是静默退出）。
//
// 只有 `-tags desktop` 的 Windows 构建才编入内嵌资源：服务器形态的
// managi-windows-amd64.exe 因此不必先准备 index.html / icon.ico，
// 缺失内嵌资源也不会让它构建失败。两种形态共用同一个 main（cmd/managi -tray）。
package desktop
