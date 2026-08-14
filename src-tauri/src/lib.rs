use std::net::SocketAddr;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;

use serde::Deserialize;
use tauri::menu::{Menu, MenuItem};
use tauri::tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent};
use tauri::{AppHandle, Manager, State, WebviewUrl, WebviewWindowBuilder, WindowEvent};
use tauri_plugin_shell::{
    process::{CommandChild, CommandEvent},
    ShellExt,
};

mod system_integration;

/// True once the user asked the app to quit from the tray. Closing the window
/// hides it to the tray instead of exiting, unless a real quit is in progress.
static QUITTING: AtomicBool = AtomicBool::new(false);

struct CoreProcess(Mutex<Option<CommandChild>>);

#[derive(Deserialize)]
struct ModelBundle {
    active_model: Option<String>,
}

#[tauri::command]
fn start_core(
    app: AppHandle,
    state: State<'_, CoreProcess>,
    address: String,
    proxy_address: String,
    forward_proxy_address: String,
) -> Result<(), String> {
    spawn_core(
        &app,
        state.inner(),
        &address,
        &proxy_address,
        &forward_proxy_address,
    )
}

fn spawn_core(
    app: &AppHandle,
    state: &CoreProcess,
    address: &str,
    proxy_address: &str,
    forward_proxy_address: &str,
) -> Result<(), String> {
    let listen_address: SocketAddr = address
        .parse()
        .map_err(|_| "core address must be an IP address and port")?;
    if !listen_address.ip().is_loopback() || listen_address.port() == 0 {
        return Err("desktop core must listen on a loopback address and non-zero port".to_string());
    }
    let proxy_listen_address: SocketAddr = proxy_address
        .parse()
        .map_err(|_| "proxy address must be an IP address and port")?;
    if !proxy_listen_address.ip().is_loopback() || proxy_listen_address.port() == 0 {
        return Err(
            "desktop proxy must listen on a loopback address and non-zero port".to_string(),
        );
    }
    if proxy_listen_address == listen_address {
        return Err("core and proxy addresses must use different ports".to_string());
    }
    let forward_proxy_listen_address: SocketAddr = forward_proxy_address
        .parse()
        .map_err(|_| "forward proxy address must be an IP address and port")?;
    if !forward_proxy_listen_address.ip().is_loopback() || forward_proxy_listen_address.port() == 0
    {
        return Err(
            "desktop forward proxy must listen on a loopback address and non-zero port".to_string(),
        );
    }
    if forward_proxy_listen_address == listen_address
        || forward_proxy_listen_address == proxy_listen_address
    {
        return Err(
            "core, gateway, and forward proxy addresses must use different ports".to_string(),
        );
    }

    let mut process = state.0.lock().map_err(|_| "core process lock poisoned")?;
    if process.is_some() {
        return Ok(());
    }

    let mut args = vec!["--addr".to_string(), listen_address.to_string()];
    args.push("--proxy-addr".to_string());
    args.push(proxy_listen_address.to_string());
    args.push("--forward-proxy-addr".to_string());
    args.push(forward_proxy_listen_address.to_string());
    let resource_dir = app
        .path()
        .resource_dir()
        .map_err(|error| error.to_string())?;
    let bundled_resources = resource_dir.join("resources");
    let home_dir = app.path().home_dir().map_err(|error| error.to_string())?;
    let remask_dir = home_dir.join(".remask");
    std::fs::create_dir_all(&remask_dir).map_err(|error| error.to_string())?;
    // Bundled models are the initial seed; downloaded models must live in the
    // writable per-user directory rather than inside the application bundle.
    let models_dir = remask_dir.join("models");
    if !models_dir.exists() {
        if let Err(error) = copy_dir_all(&bundled_resources.join("models"), &models_dir) {
            if error.kind() != std::io::ErrorKind::NotFound {
                return Err(error.to_string());
            }
            std::fs::create_dir_all(&models_dir).map_err(|error| error.to_string())?;
        }
    }
    args.push("--data-dir".to_string());
    args.push(remask_dir.to_string_lossy().into_owned());
    args.push("--models-dir".to_string());
    args.push(models_dir.to_string_lossy().into_owned());
    if let Some(bundled_model_id) = bundled_active_model(&bundled_resources) {
        // A user model directory can already exist without the bundled model.
        // Only request activation when that concrete package is installed.
        if models_dir
            .join(&bundled_model_id)
            .join("manifest.json")
            .is_file()
        {
            args.push("--active-model".to_string());
            args.push(bundled_model_id);
        }
    }
    if let Some(runtime_library) = find_runtime_library(&bundled_resources) {
        args.push("--onnxruntime-lib".to_string());
        args.push(runtime_library.to_string_lossy().into_owned());
    }

    let command = app
        .shell()
        .sidecar("remask-core")
        .map_err(|error| error.to_string())?
        .args(args);
    let (mut events, child) = command.spawn().map_err(|error| error.to_string())?;
    let pid = child.pid();
    *process = Some(child);
    drop(process);

    let app_handle = app.clone();
    tauri::async_runtime::spawn(async move {
        while let Some(event) = events.recv().await {
            match event {
                CommandEvent::Stdout(line) => {
                    eprintln!("remask-core: {}", String::from_utf8_lossy(&line));
                }
                CommandEvent::Stderr(line) => {
                    eprintln!("remask-core: {}", String::from_utf8_lossy(&line));
                }
                CommandEvent::Error(error) => {
                    eprintln!("remask-core event error: {error}");
                }
                CommandEvent::Terminated(payload) => {
                    eprintln!(
                        "remask-core exited code={:?} signal={:?}",
                        payload.code, payload.signal
                    );
                    let state = app_handle.state::<CoreProcess>();
                    if let Ok(mut current) = state.0.lock() {
                        if current.as_ref().is_some_and(|child| child.pid() == pid) {
                            current.take();
                        }
                    };
                }
                _ => {}
            }
        }
    });
    Ok(())
}

fn copy_dir_all(source: &Path, destination: &Path) -> std::io::Result<()> {
    std::fs::create_dir_all(destination)?;
    for entry in std::fs::read_dir(source)? {
        let entry = entry?;
        let target = destination.join(entry.file_name());
        if entry.file_type()?.is_dir() {
            copy_dir_all(&entry.path(), &target)?;
        } else {
            std::fs::copy(entry.path(), target)?;
        }
    }
    Ok(())
}

fn bundled_active_model(resources_dir: &Path) -> Option<String> {
    let content = std::fs::read_to_string(resources_dir.join("model-bundle.json")).ok()?;
    let bundle: ModelBundle = serde_json::from_str(&content).ok()?;
    bundle.active_model.filter(|id| is_model_id(id))
}

fn is_model_id(id: &str) -> bool {
    !id.is_empty()
        && id
            .bytes()
            .all(|value| value.is_ascii_alphanumeric() || matches!(value, b'-' | b'_' | b'.'))
}

fn find_runtime_library(resource_dir: &Path) -> Option<PathBuf> {
    let filename = if cfg!(target_os = "macos") {
        "libonnxruntime.dylib"
    } else if cfg!(target_os = "windows") {
        "onnxruntime.dll"
    } else {
        "libonnxruntime.so"
    };
    let candidate = resource_dir.join("onnxruntime").join(filename);
    candidate.is_file().then_some(candidate)
}

#[tauri::command]
fn stop_core(state: State<'_, CoreProcess>) -> Result<(), String> {
    let mut process = state.0.lock().map_err(|_| "core process lock poisoned")?;
    if let Some(child) = process.take() {
        child.kill().map_err(|error| error.to_string())?;
    }
    Ok(())
}

#[tauri::command]
fn system_certificate_status(
    app: AppHandle,
) -> Result<system_integration::CertificateTrustStatus, String> {
    system_integration::certificate_status(&app)
}

#[tauri::command]
async fn install_system_certificate(
    app: AppHandle,
) -> Result<system_integration::CertificateTrustStatus, String> {
    tauri::async_runtime::spawn_blocking(move || system_integration::install_certificate(&app))
        .await
        .map_err(|error| error.to_string())?
}

#[tauri::command]
fn launch_ai_client(
    app: AppHandle,
    client: String,
    forward_proxy_address: String,
) -> Result<(), String> {
    system_integration::launch_client(&app, &client, &forward_proxy_address)
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_autostart::init(
            tauri_plugin_autostart::MacosLauncher::LaunchAgent,
            Some(vec!["--autostart"]),
        ))
        .manage(CoreProcess(Mutex::new(None)))
        .setup(|app| {
            let launched_at_login = std::env::args().any(|arg| arg == "--autostart");

            let window = if let Some(window) = app.get_webview_window("main") {
                window
            } else {
                WebviewWindowBuilder::new(app, "main", WebviewUrl::App("index.html".into()))
                    .title("Remask")
                    .inner_size(1040.0, 650.0)
                    .min_inner_size(640.0, 560.0)
                    .visible(false)
                    .build()?
            };

            // System tray icon: left-click shows the window, the menu offers
            // show and a real quit (closing the window only hides to the tray).
            // The tray uses a monochrome mark; macOS renders it as a template
            // image so it adapts to light and dark menu bars.
            let show_item = MenuItem::with_id(app, "show", "显示主窗口", true, None::<&str>)?;
            let quit_item = MenuItem::with_id(app, "quit", "退出", true, None::<&str>)?;
            let menu = Menu::with_items(app, &[&show_item, &quit_item])?;
            let tray_icon = tauri::include_image!("icons/tray-icon.png");
            let _tray = TrayIconBuilder::new()
                .icon(tray_icon)
                .icon_as_template(cfg!(target_os = "macos"))
                .menu(&menu)
                .show_menu_on_left_click(false)
                .on_menu_event(|app, event| match event.id().as_ref() {
                    "show" => show_main_window(app),
                    "quit" => {
                        QUITTING.store(true, Ordering::Relaxed);
                        app.exit(0);
                    }
                    _ => {}
                })
                .on_tray_icon_event(|tray, event| {
                    if let TrayIconEvent::Click {
                        button: MouseButton::Left,
                        button_state: MouseButtonState::Up,
                        ..
                    } = event
                    {
                        show_main_window(tray.app_handle());
                    }
                })
                .build(app)?;

            // Launched at login: run minimized in the tray. Manual launches
            // show the main window as usual.
            if launched_at_login {
                let _ = window.hide();
            } else {
                window.show()?;
                window.unminimize()?;
                window.set_focus()?;
            }
            if let Err(error) = spawn_core(
                app.handle(),
                app.state::<CoreProcess>().inner(),
                "127.0.0.1:17680",
                "127.0.0.1:17681",
                "127.0.0.1:17682",
            ) {
                eprintln!("failed to start remask-core: {error}");
            }
            Ok(())
        })
        .on_window_event(|window, event| {
            if let WindowEvent::CloseRequested { api, .. } = event {
                if window.label() == "main" && !QUITTING.load(Ordering::Relaxed) {
                    api.prevent_close();
                    let _ = window.hide();
                }
            }
        })
        .invoke_handler(tauri::generate_handler![
            start_core,
            stop_core,
            system_certificate_status,
            install_system_certificate,
            launch_ai_client
        ])
        .run(tauri::generate_context!())
        .expect("error while running remask-desktop");
}

fn show_main_window(app: &AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.show();
        let _ = window.unminimize();
        let _ = window.set_focus();
    }
}
