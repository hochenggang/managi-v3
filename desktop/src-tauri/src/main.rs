// Tauri 桌面客户端入口。
// 职责：spawn Go sidecar → 等待健康检查 → 打开 WebView → 系统托盘。
// 设计见 ../../../design-v3.md 第七章。

#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use std::process::Command;
use std::time::Duration;
use tauri::{Manager, SystemTray, SystemTrayMenu, SystemTrayMenuItem, CustomMenuItem};

fn main() {
    let tray = SystemTray::new().with_menu(
        SystemTrayMenu::new()
            .add_item(CustomMenuItem::new("open", "打开 Managi"))
            .add_native_item(SystemTrayMenuItem::Separator)
            .add_item(CustomMenuItem::new("quit", "退出")),
    );

    tauri::Builder::default()
        .system_tray(tray)
        .on_system_tray_event(|app, event| {
            // TODO(P2): 处理托盘事件：open 显示主窗口，quit 退出并 kill sidecar
            if let tauri::SystemTrayEvent::MenuItemClick { id, .. } = event {
                match id.as_str() {
                    "open" => {
                        let _ = app.get_window("main").map(|w| w.show());
                    }
                    "quit" => {
                        // TODO(P2): 发送 SIGTERM 给 sidecar 进程
                        app.exit(0);
                    }
                    _ => {}
                }
            }
        })
        .setup(|app| {
            // spawn Go sidecar（监听 127.0.0.1:18001）
            // TODO(P2): 用 tauri::api::process::Command::new_sidecar("managi") 启动
            //   并在端口冲突时探测 18001-18100
            // TODO(P2): 轮询 http://127.0.0.1:18001/health 直到就绪
            // TODO(P2): 就绪后 WebView 自动加载 frontendDist（已配置）
            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
