use std::net::{SocketAddr, TcpStream};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;
use std::thread;
use std::time::{Duration, Instant};

use tauri::menu::{Menu, MenuItem};
use tauri::tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent};
use tauri::{AppHandle, Manager, RunEvent, State, WebviewUrl, WebviewWindowBuilder, WindowEvent};
use tauri_plugin_shell::{
    process::{CommandChild, CommandEvent},
    ShellExt,
};

mod system_integration;

/// True once the user asked the app to quit from the tray. Closing the window
/// hides it to the tray instead of exiting, unless a real quit is in progress.
static QUITTING: AtomicBool = AtomicBool::new(false);

struct CoreProcess(Mutex<Option<ManagedCoreProcess>>);

struct ManagedCoreProcess {
    child: CommandChild,
    #[cfg(target_os = "windows")]
    _kill_on_close_job: Option<WindowsKillOnCloseJob>,
}

impl ManagedCoreProcess {
    fn pid(&self) -> u32 {
        self.child.pid()
    }
}

#[cfg(target_os = "windows")]
struct WindowsKillOnCloseJob(windows_sys::Win32::Foundation::HANDLE);

#[cfg(target_os = "windows")]
unsafe impl Send for WindowsKillOnCloseJob {}

#[cfg(target_os = "windows")]
impl Drop for WindowsKillOnCloseJob {
    fn drop(&mut self) {
        unsafe {
            windows_sys::Win32::Foundation::CloseHandle(self.0);
        }
    }
}

#[cfg(target_os = "windows")]
fn create_kill_on_close_job(pid: u32) -> Result<WindowsKillOnCloseJob, String> {
    use std::mem::size_of;
    use std::ptr::null;
    use windows_sys::Win32::Foundation::CloseHandle;
    use windows_sys::Win32::System::JobObjects::{
        AssignProcessToJobObject, CreateJobObjectW, JobObjectExtendedLimitInformation,
        SetInformationJobObject, JOBOBJECT_EXTENDED_LIMIT_INFORMATION,
        JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
    };
    use windows_sys::Win32::System::Threading::{
        OpenProcess, PROCESS_SET_QUOTA, PROCESS_TERMINATE,
    };

    unsafe {
        let job = CreateJobObjectW(null(), null());
        if job.is_null() {
            return Err(std::io::Error::last_os_error().to_string());
        }

        let mut limits = JOBOBJECT_EXTENDED_LIMIT_INFORMATION::default();
        limits.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;
        if SetInformationJobObject(
            job,
            JobObjectExtendedLimitInformation,
            (&raw const limits).cast(),
            size_of::<JOBOBJECT_EXTENDED_LIMIT_INFORMATION>() as u32,
        ) == 0
        {
            let error = std::io::Error::last_os_error().to_string();
            CloseHandle(job);
            return Err(error);
        }

        let process = OpenProcess(PROCESS_SET_QUOTA | PROCESS_TERMINATE, 0, pid);
        if process.is_null() {
            let error = std::io::Error::last_os_error().to_string();
            CloseHandle(job);
            return Err(error);
        }
        let assigned = AssignProcessToJobObject(job, process);
        let assignment_error = (assigned == 0).then(|| std::io::Error::last_os_error().to_string());
        CloseHandle(process);
        if let Some(error) = assignment_error {
            CloseHandle(job);
            return Err(error);
        }

        Ok(WindowsKillOnCloseJob(job))
    }
}

#[tauri::command]
fn append_client_log(app: AppHandle, entry: String) -> Result<(), String> {
    // Keep the client log in the same per-user directory as the Core data,
    // without exposing a filesystem path to the webview.
    let log_dir = app
        .path()
        .home_dir()
        .map_err(|error| error.to_string())?
        .join(".remask")
        .join("logs");
    std::fs::create_dir_all(&log_dir).map_err(|error| error.to_string())?;
    let log_path = log_dir.join("client.log");
    const MAX_CLIENT_LOG_BYTES: u64 = 2 * 1024 * 1024;
    if let Ok(metadata) = std::fs::metadata(&log_path) {
        if metadata.len() >= MAX_CLIENT_LOG_BYTES {
            let rotated_path = log_dir.join("client.log.1");
            let _ = std::fs::remove_file(&rotated_path);
            std::fs::rename(&log_path, rotated_path).map_err(|error| error.to_string())?;
        }
    }
    // Parse and re-serialize so one entry always occupies exactly one JSONL
    // line even if a caller supplies embedded newlines.
    let parsed: serde_json::Value =
        serde_json::from_str(&entry).map_err(|error| error.to_string())?;
    let payload = serde_json::to_string(&parsed).map_err(|error| error.to_string())?;
    let mut file = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(log_path)
        .map_err(|error| error.to_string())?;
    use std::io::Write;
    writeln!(file, "{payload}").map_err(|error| error.to_string())
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
    #[cfg(target_os = "windows")]
    let kill_on_close_job = match create_kill_on_close_job(pid) {
        Ok(job) => Some(job),
        Err(error) => {
            eprintln!("failed to bind remask-core to the desktop process: {error}");
            None
        }
    };
    *process = Some(ManagedCoreProcess {
        child,
        #[cfg(target_os = "windows")]
        _kill_on_close_job: kill_on_close_job,
    });
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
                        if current.as_ref().is_some_and(|process| process.pid() == pid) {
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
    stop_core_process(state.inner())?;
    Ok(())
}

fn stop_core_process(state: &CoreProcess) -> Result<bool, String> {
    let mut process = state.0.lock().map_err(|_| "core process lock poisoned")?;
    if let Some(process) = process.take() {
        process.child.kill().map_err(|error| error.to_string())?;
        return Ok(true);
    }
    Ok(false)
}

/// Stop-and-start must not race the old process while its listeners are still
/// being torn down. Wait until all configured ports reject connections before
/// spawning the replacement.
fn wait_for_ports_to_close(addresses: &[SocketAddr]) {
    let deadline = Instant::now() + Duration::from_secs(3);
    while Instant::now() < deadline {
        if addresses
            .iter()
            .all(|address| TcpStream::connect_timeout(address, Duration::from_millis(50)).is_err())
        {
            return;
        }
        thread::sleep(Duration::from_millis(50));
    }
}

#[tauri::command]
fn restart_core(
    app: AppHandle,
    state: State<'_, CoreProcess>,
    address: String,
    proxy_address: String,
    forward_proxy_address: String,
) -> Result<(), String> {
    // Validate the values before stopping the currently healthy Core.
    let addresses = [
        address.parse::<SocketAddr>(),
        proxy_address.parse::<SocketAddr>(),
        forward_proxy_address.parse::<SocketAddr>(),
    ];
    let addresses = addresses
        .into_iter()
        .collect::<Result<Vec<_>, _>>()
        .map_err(|_| "core addresses must be IP addresses and ports".to_string())?;

    if stop_core_process(state.inner())? {
        wait_for_ports_to_close(&addresses);
    }
    spawn_core(
        &app,
        state.inner(),
        &address,
        &proxy_address,
        &forward_proxy_address,
    )
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
                        if let Err(error) = stop_core_process(app.state::<CoreProcess>().inner()) {
                            eprintln!("failed to stop remask-core before exit: {error}");
                        }
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
                // Keep a background launch out of the Dock while retaining the
                // menu-bar item that can restore the window.
                set_dock_visibility(app.handle(), false);
                let _ = window.hide();
            } else {
                window.show()?;
                window.unminimize()?;
                window.set_focus()?;
            }
            // Preparing the writable model directory can be expensive on a
            // first launch. Do it off the setup thread so the webview can
            // render the complete client immediately, independently of Core.
            let app_handle = app.handle().clone();
            tauri::async_runtime::spawn_blocking(move || {
                if let Err(error) = spawn_core(
                    &app_handle,
                    app_handle.state::<CoreProcess>().inner(),
                    "127.0.0.1:17680",
                    "127.0.0.1:17681",
                    "127.0.0.1:17682",
                ) {
                    eprintln!("failed to start remask-core: {error}");
                }
            });
            Ok(())
        })
        .on_window_event(|window, event| {
            if let WindowEvent::CloseRequested { api, .. } = event {
                if window.label() == "main" && !QUITTING.load(Ordering::Relaxed) {
                    api.prevent_close();
                    // Hiding the Dock item is separate from hiding the window:
                    // this leaves the tray available as the way back in.
                    set_dock_visibility(window.app_handle(), false);
                    let _ = window.hide();
                }
            }
        })
        .invoke_handler(tauri::generate_handler![
            append_client_log,
            start_core,
            stop_core,
            restart_core,
            system_certificate_status,
            install_system_certificate,
            launch_ai_client
        ])
        .build(tauri::generate_context!())
        .expect("error while building remask-desktop")
        .run(|app, event| {
            if matches!(event, RunEvent::Exit) {
                if let Err(error) = stop_core_process(app.state::<CoreProcess>().inner()) {
                    eprintln!("failed to stop remask-core during exit: {error}");
                }
            }
        });
}

fn show_main_window(app: &AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        // Restore the regular foreground application before showing the
        // window. Tauri's macOS implementation preserves the app icon while
        // switching it back from the menu-bar-only state.
        set_dock_visibility(app, true);
        let _ = window.show();
        let _ = window.unminimize();
        let _ = window.set_focus();
    }
}

/// Toggle the macOS Dock item without changing the tray icon. On other
/// platforms this is intentionally a no-op so the window lifecycle remains
/// shared by all desktop targets.
fn set_dock_visibility(app: &AppHandle, visible: bool) {
    #[cfg(target_os = "macos")]
    if let Err(error) = app.set_dock_visibility(visible) {
        eprintln!("failed to set Dock visibility to {visible}: {error}");
    }
}
