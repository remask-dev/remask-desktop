use std::net::SocketAddr;
use std::path::{Path, PathBuf};
use std::sync::Mutex;

use serde::Deserialize;
use tauri::{AppHandle, Manager, State};
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
    let models_dir = bundled_resources.join("models");
    let home_dir = app.path().home_dir().map_err(|error| error.to_string())?;
    let remask_dir = home_dir.join(".remask");
    std::fs::create_dir_all(&remask_dir).map_err(|error| error.to_string())?;
    args.push("--data-dir".to_string());
    args.push(remask_dir.to_string_lossy().into_owned());
    if models_dir.is_dir() {
        args.push("--models-dir".to_string());
        args.push(models_dir.to_string_lossy().into_owned());
    }
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
    if let Some(child) = process.as_mut() {
        child.kill().map_err(|error| error.to_string())?;
    }
    *process = None;
    Ok(())
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .manage(CoreProcess(Mutex::new(None)))
        .invoke_handler(tauri::generate_handler![start_core, stop_core])
        .run(tauri::generate_context!())
        .expect("error while running remask-desktop");
}
