use std::net::SocketAddr;
use std::path::{Path, PathBuf};
use std::sync::Mutex;

use serde::Deserialize;
use tauri::{AppHandle, Manager, State, WebviewUrl, WebviewWindowBuilder};
use tauri_plugin_shell::{process::CommandChild, ShellExt};

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
        return Err("desktop proxy must listen on a loopback address and non-zero port".to_string());
    }
    if proxy_listen_address == listen_address {
        return Err("core and proxy addresses must use different ports".to_string());
    }

    let mut process = state.0.lock().map_err(|_| "core process lock poisoned")?;
    if process.is_some() {
        return Ok(());
    }

    let mut args = vec!["--addr".to_string(), listen_address.to_string()];
    args.push("--proxy-addr".to_string());
    args.push(proxy_listen_address.to_string());
    let resource_dir = app.path().resource_dir().map_err(|error| error.to_string())?;
    let bundled_resources = resource_dir.join("resources");
    let home_dir = app.path().home_dir().map_err(|error| error.to_string())?;
    let remask_dir = home_dir.join(".remask");
    std::fs::create_dir_all(&remask_dir).map_err(|error| error.to_string())?;
    // Bundled models are the initial seed; downloaded models must live in the
    // writable per-user directory rather than inside the application bundle.
    let models_dir = remask_dir.join("models");
    if !models_dir.exists() {
        if let Err(error) = copy_dir_all(&bundled_resources.join("models"), &models_dir) {
            if error.kind() != std::io::ErrorKind::NotFound { return Err(error.to_string()); }
            std::fs::create_dir_all(&models_dir).map_err(|error| error.to_string())?;
        }
    }
    args.push("--data-dir".to_string());
    args.push(remask_dir.to_string_lossy().into_owned());
    args.push("--models-dir".to_string());
    args.push(models_dir.to_string_lossy().into_owned());
    if let Some(bundled_model_id) = bundled_active_model(&bundled_resources) {
        args.push("--active-model".to_string());
        args.push(bundled_model_id);
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
    let (_events, child) = command.spawn().map_err(|error| error.to_string())?;
    *process = Some(child);
    Ok(())
}

fn copy_dir_all(source: &Path, destination: &Path) -> std::io::Result<()> {
    std::fs::create_dir_all(destination)?;
    for entry in std::fs::read_dir(source)? {
        let entry = entry?;
        let target = destination.join(entry.file_name());
        if entry.file_type()?.is_dir() { copy_dir_all(&entry.path(), &target)?; }
        else { std::fs::copy(entry.path(), target)?; }
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

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .manage(CoreProcess(Mutex::new(None)))
        .setup(|app| {
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
            window.show()?;
            window.unminimize()?;
            window.set_focus()?;
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![start_core, stop_core])
        .run(tauri::generate_context!())
        .expect("error while running remask-desktop");
}
