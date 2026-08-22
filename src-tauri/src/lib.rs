use std::fs::File;
use std::io::Write;
use std::net::{IpAddr, Ipv4Addr, SocketAddr, TcpStream};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;
use std::thread;
use std::time::{Duration, Instant};

use tauri::menu::{Menu, MenuItem, Submenu};
use tauri::tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent};
use tauri::{
    AppHandle, Emitter, Manager, RunEvent, State, WebviewUrl, WebviewWindowBuilder, WindowEvent,
};
use tauri_plugin_shell::{
    process::{CommandChild, CommandEvent},
    ShellExt,
};

const OFFICIAL_WEBSITE_URL: &str = "https://remask.dev";
const DEFAULT_PURCHASE_URL: &str = "https://remask.app/license/";

mod system_integration;

/// True once the user asked the app to quit from the tray. Closing the window
/// hides it to the tray instead of exiting, unless a real quit is in progress.
static QUITTING: AtomicBool = AtomicBool::new(false);

struct CoreProcess(Mutex<Option<ManagedCoreProcess>>);

#[derive(serde::Deserialize, serde::Serialize)]
struct GatewayAddresses {
    api_gateway: String,
    http_proxy: String,
    #[serde(default)]
    allow_lan: bool,
}

impl Default for GatewayAddresses {
    fn default() -> Self {
        Self {
            api_gateway: "127.0.0.1:17681".to_string(),
            http_proxy: "127.0.0.1:17682".to_string(),
            allow_lan: false,
        }
    }
}

fn gateway_addresses_path(app: &AppHandle) -> Result<PathBuf, String> {
    let home_dir = app.path().home_dir().map_err(|error| error.to_string())?;
    Ok(home_dir.join(".remask").join("desktop_gateway.json"))
}

fn load_gateway_addresses(app: &AppHandle) -> GatewayAddresses {
    let Ok(path) = gateway_addresses_path(app) else {
        return GatewayAddresses::default();
    };
    std::fs::read(path)
        .ok()
        .and_then(|data| serde_json::from_slice(&data).ok())
        .unwrap_or_default()
}

#[tauri::command]
fn get_gateway_settings(app: AppHandle) -> GatewayAddresses {
    load_gateway_addresses(&app)
}

#[tauri::command]
fn open_profile_directory(app: AppHandle) -> Result<(), String> {
    let home_dir = app.path().home_dir().map_err(|error| error.to_string())?;
    let profiles_dir = home_dir.join(".remask").join("profiles");
    std::fs::create_dir_all(&profiles_dir).map_err(|error| error.to_string())?;
    #[allow(deprecated)]
    app.shell()
        .open(profiles_dir.to_string_lossy().into_owned(), None)
        .map_err(|error| error.to_string())
}

fn save_gateway_addresses(
    app: &AppHandle,
    api_gateway: &str,
    http_proxy: &str,
    allow_lan: bool,
) -> Result<(), String> {
    let path = gateway_addresses_path(app)?;
    if let Some(directory) = path.parent() {
        std::fs::create_dir_all(directory).map_err(|error| error.to_string())?;
    }
    let data = serde_json::to_vec_pretty(&GatewayAddresses {
        api_gateway: api_gateway.to_string(),
        http_proxy: http_proxy.to_string(),
        allow_lan,
    })
    .map_err(|error| error.to_string())?;
    let temporary = path.with_extension("json.tmp");
    std::fs::write(&temporary, data).map_err(|error| error.to_string())?;
    std::fs::rename(temporary, path).map_err(|error| error.to_string())
}

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
    writeln!(file, "{payload}").map_err(|error| error.to_string())
}

fn open_core_log(remask_dir: &Path) -> Result<File, String> {
    let log_dir = remask_dir.join("logs");
    std::fs::create_dir_all(&log_dir).map_err(|error| error.to_string())?;
    std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(log_dir.join("core.log"))
        .map_err(|error| error.to_string())
}

fn persist_core_log_line(log: &mut Option<File>, line: &[u8]) {
    let result = log.as_mut().map(|file| {
        file.write_all(line)?;
        if !line.ends_with(b"\n") {
            file.write_all(b"\n")?;
        }
        file.flush()
    });
    if let Some(Err(error)) = result {
        eprintln!("failed to write core.log: {error}");
        *log = None;
    }
}

#[tauri::command]
fn start_core(
    app: AppHandle,
    state: State<'_, CoreProcess>,
    address: String,
    proxy_address: String,
    forward_proxy_address: String,
    allow_lan: Option<bool>,
) -> Result<(), String> {
    let allow_lan = allow_lan.unwrap_or_else(|| load_gateway_addresses(&app).allow_lan);
    spawn_core(
        &app,
        state.inner(),
        &address,
        &proxy_address,
        &forward_proxy_address,
        allow_lan,
    )?;
    save_gateway_addresses(&app, &proxy_address, &forward_proxy_address, allow_lan)
}

fn spawn_core(
    app: &AppHandle,
    state: &CoreProcess,
    address: &str,
    proxy_address: &str,
    forward_proxy_address: &str,
    allow_lan: bool,
) -> Result<(), String> {
    let [listen_address, proxy_listen_address, forward_proxy_listen_address] =
        resolve_core_addresses(address, proxy_address, forward_proxy_address, allow_lan)?;

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
    // Downloaded models live in the writable per-user directory. Bundled
    // models stay in the application resources and are scanned read-only, so
    // startup never duplicates the large model files into the user's home.
    let models_dir = remask_dir.join("models");
    std::fs::create_dir_all(&models_dir).map_err(|error| error.to_string())?;
    args.push("--data-dir".to_string());
    args.push(remask_dir.to_string_lossy().into_owned());
    args.push("--models-dir".to_string());
    args.push(models_dir.to_string_lossy().into_owned());
    let builtin_models_dir = bundled_resources.join("models");
    if builtin_models_dir.is_dir() {
        args.push("--builtin-models-dir".to_string());
        args.push(builtin_models_dir.to_string_lossy().into_owned());
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
    let mut core_log = match open_core_log(&remask_dir) {
        Ok(file) => Some(file),
        Err(error) => {
            eprintln!("failed to open core.log: {error}");
            None
        }
    };
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
                    persist_core_log_line(&mut core_log, &line);
                }
                CommandEvent::Stderr(line) => {
                    eprintln!("remask-core: {}", String::from_utf8_lossy(&line));
                    persist_core_log_line(&mut core_log, &line);
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

fn resolve_core_addresses(
    address: &str,
    proxy_address: &str,
    forward_proxy_address: &str,
    allow_lan: bool,
) -> Result<[SocketAddr; 3], String> {
    let listen_address: SocketAddr = address
        .parse()
        .map_err(|_| "core address must be an IP address and port")?;
    if !listen_address.ip().is_loopback() || listen_address.port() == 0 {
        return Err("desktop core must listen on a loopback address and non-zero port".to_string());
    }
    let mut proxy_listen_address: SocketAddr = proxy_address
        .parse()
        .map_err(|_| "proxy address must be an IP address and port")?;
    if !proxy_listen_address.ip().is_loopback() || proxy_listen_address.port() == 0 {
        return Err(
            "desktop proxy must use a loopback address and non-zero port in its configuration"
                .to_string(),
        );
    }
    let mut forward_proxy_listen_address: SocketAddr = forward_proxy_address
        .parse()
        .map_err(|_| "proxy gateway address must be an IP address and port")?;
    if !forward_proxy_listen_address.ip().is_loopback() || forward_proxy_listen_address.port() == 0
    {
        return Err(
            "desktop proxy gateway must use a loopback address and non-zero port in its configuration"
                .to_string(),
        );
    }
    if listen_address.port() == proxy_listen_address.port()
        || listen_address.port() == forward_proxy_listen_address.port()
        || proxy_listen_address.port() == forward_proxy_listen_address.port()
    {
        return Err(
            "core, API gateway, and proxy gateway addresses must use different ports".to_string(),
        );
    }
    if allow_lan {
        proxy_listen_address.set_ip(IpAddr::V4(Ipv4Addr::UNSPECIFIED));
        forward_proxy_listen_address.set_ip(IpAddr::V4(Ipv4Addr::UNSPECIFIED));
    }
    Ok([
        listen_address,
        proxy_listen_address,
        forward_proxy_listen_address,
    ])
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
    allow_lan: Option<bool>,
) -> Result<(), String> {
    let allow_lan = allow_lan.unwrap_or_else(|| load_gateway_addresses(&app).allow_lan);
    // Validate the values before stopping the currently healthy Core.
    let addresses =
        resolve_core_addresses(&address, &proxy_address, &forward_proxy_address, allow_lan)?;

    if stop_core_process(state.inner())? {
        wait_for_ports_to_close(&addresses);
    }
    spawn_core(
        &app,
        state.inner(),
        &address,
        &proxy_address,
        &forward_proxy_address,
        allow_lan,
    )?;
    save_gateway_addresses(&app, &proxy_address, &forward_proxy_address, allow_lan)
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
async fn uninstall_system_certificate(
    app: AppHandle,
) -> Result<system_integration::CertificateTrustStatus, String> {
    tauri::async_runtime::spawn_blocking(move || system_integration::uninstall_certificate(&app))
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

#[tauri::command]
fn launch_app_with_proxy(
    app: AppHandle,
    app_path: String,
    forward_proxy_address: String,
) -> Result<(), String> {
    system_integration::launch_app(&app, &app_path, &forward_proxy_address)
}

#[tauri::command]
fn launch_preset_with_proxy(
    app: AppHandle,
    preset: String,
    forward_proxy_address: String,
) -> Result<(), String> {
    system_integration::launch_preset(&app, &preset, &forward_proxy_address)
}

#[tauri::command]
fn open_purchase_page(app: AppHandle, device_id: String) -> Result<(), String> {
    if !device_id.starts_with("RMK1-")
        || device_id.len() > 64
        || !device_id.chars().all(|character| {
            character.is_ascii_uppercase() || character.is_ascii_digit() || character == '-'
        })
    {
        return Err("invalid Remask device ID".to_string());
    }
    let base_url = option_env!("REMASK_PURCHASE_URL").unwrap_or(DEFAULT_PURCHASE_URL);
    let separator = if base_url.contains('?') { '&' } else { '?' };
    let url = format!("{base_url}{separator}product=remask-desktop&device_id={device_id}");
    #[allow(deprecated)]
    app.shell()
        .open(url, None)
        .map_err(|error| error.to_string())
}

#[tauri::command]
fn open_official_website(app: AppHandle) -> Result<(), String> {
    #[allow(deprecated)]
    app.shell()
        .open(OFFICIAL_WEBSITE_URL, None)
        .map_err(|error| error.to_string())
}

#[tauri::command]
fn set_tray_locale(app: AppHandle, locale: String) -> Result<(), String> {
    let tray = app
        .tray_by_id("main")
        .ok_or_else(|| "system tray is unavailable".to_string())?;
    let menu = build_tray_menu(&app, &locale).map_err(|error| error.to_string())?;
    tray.set_menu(Some(menu)).map_err(|error| error.to_string())
}

struct TrayLabels {
    show_main_window: &'static str,
    terminal: &'static str,
    browser: &'static str,
    choose_another_app: &'static str,
    protected_launch: &'static str,
    copy_environment: &'static str,
    quit: &'static str,
}

const TRAY_LABELS_EN: TrayLabels = TrayLabels {
    show_main_window: "Show Main Window",
    terminal: "Terminal",
    browser: "Browser",
    choose_another_app: "Choose Another App…",
    protected_launch: "Protected Launch",
    copy_environment: "Copy Environment Variables",
    quit: "Quit",
};
const TRAY_LABELS_ZH: TrayLabels = TrayLabels {
    show_main_window: "显示主窗口",
    terminal: "终端",
    browser: "浏览器",
    choose_another_app: "选择其他应用…",
    protected_launch: "安全启动",
    copy_environment: "复制环境变量",
    quit: "退出",
};
const TRAY_LABELS_JA: TrayLabels = TrayLabels {
    show_main_window: "メインウィンドウを表示",
    terminal: "ターミナル",
    browser: "ブラウザ",
    choose_another_app: "別のアプリを選択…",
    protected_launch: "保護された起動",
    copy_environment: "環境変数をコピー",
    quit: "終了",
};
const TRAY_LABELS_DE: TrayLabels = TrayLabels {
    show_main_window: "Hauptfenster anzeigen",
    terminal: "Terminal",
    browser: "Browser",
    choose_another_app: "Andere App auswählen…",
    protected_launch: "Geschützter Start",
    copy_environment: "Umgebungsvariablen kopieren",
    quit: "Beenden",
};

fn tray_labels(locale: &str) -> &'static TrayLabels {
    match locale {
        "zh" => &TRAY_LABELS_ZH,
        "ja" => &TRAY_LABELS_JA,
        "de" => &TRAY_LABELS_DE,
        _ => &TRAY_LABELS_EN,
    }
}

fn build_tray_menu(app: &AppHandle, locale: &str) -> tauri::Result<Menu<tauri::Wry>> {
    let labels = tray_labels(locale);
    let show_item = MenuItem::with_id(app, "show", labels.show_main_window, true, None::<&str>)?;
    let claude_item = MenuItem::with_id(
        app,
        "safe-launch-claude-code",
        "Claude Code",
        true,
        None::<&str>,
    )?;
    let codex_item = MenuItem::with_id(app, "safe-launch-codex", "Codex", true, None::<&str>)?;
    let codex_cli_item = MenuItem::with_id(
        app,
        "safe-launch-codex-cli",
        "Codex CLI",
        true,
        None::<&str>,
    )?;
    let opencode_item =
        MenuItem::with_id(app, "safe-launch-opencode", "OpenCode", true, None::<&str>)?;
    let terminal_item = MenuItem::with_id(
        app,
        "safe-launch-terminal",
        labels.terminal,
        true,
        None::<&str>,
    )?;
    let browser_item = MenuItem::with_id(
        app,
        "safe-launch-browser",
        labels.browser,
        true,
        None::<&str>,
    )?;
    let other_app_item = MenuItem::with_id(
        app,
        "safe-launch-other",
        labels.choose_another_app,
        true,
        None::<&str>,
    )?;
    let safe_launch_menu = Submenu::with_id_and_items(
        app,
        "safe-launch",
        labels.protected_launch,
        true,
        &[
            &claude_item,
            &codex_item,
            &codex_cli_item,
            &opencode_item,
            &terminal_item,
            &browser_item,
            &other_app_item,
        ],
    )?;
    let copy_environment_item = MenuItem::with_id(
        app,
        "copy-environment",
        labels.copy_environment,
        true,
        None::<&str>,
    )?;
    let quit_item = MenuItem::with_id(app, "quit", labels.quit, true, None::<&str>)?;
    Menu::with_items(
        app,
        &[
            &show_item,
            &safe_launch_menu,
            &copy_environment_item,
            &quit_item,
        ],
    )
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_autostart::init(
            tauri_plugin_autostart::MacosLauncher::LaunchAgent,
            Some(vec!["--autostart"]),
        ))
        .plugin(tauri_plugin_dialog::init())
        .manage(CoreProcess(Mutex::new(None)))
        .setup(|app| {
            let launched_at_login = std::env::args().any(|arg| arg == "--autostart");

            let window = if let Some(window) = app.get_webview_window("main") {
                window
            } else {
                WebviewWindowBuilder::new(app, "main", WebviewUrl::App("index.html".into()))
                    .title("Remask")
                    .inner_size(1000.0, 680.0)
                    .min_inner_size(794.0, 600.0)
                    .visible(false)
                    .build()?
            };

            // macOS may restore the last window frame instead of applying the
            // configured minimum. Re-apply it at runtime so the overview fits.
            window.set_min_size(Some(tauri::LogicalSize::new(794.0, 600.0)))?;
            window.set_size(tauri::LogicalSize::new(1000.0, 680.0))?;
            window.center()?;

            // System tray icon: left-click shows the window, the menu offers
            // show and a real quit (closing the window only hides to the tray).
            // The tray uses a monochrome mark; macOS renders it as a template
            // image so it adapts to light and dark menu bars.
            let menu = build_tray_menu(app.handle(), "en")?;
            let tray_icon = tauri::include_image!("icons/tray-icon.png");
            let _tray = TrayIconBuilder::with_id("main")
                .icon(tray_icon)
                .icon_as_template(cfg!(target_os = "macos"))
                .menu(&menu)
                .show_menu_on_left_click(false)
                .on_menu_event(|app, event| match event.id().as_ref() {
                    "show" => show_main_window(app),
                    "safe-launch-claude-code" => emit_tray_launch(app, "claude-code"),
                    "safe-launch-opencode" => emit_tray_launch(app, "opencode"),
                    "safe-launch-codex" => emit_tray_launch(app, "codex"),
                    "safe-launch-codex-cli" => emit_tray_launch(app, "codex-cli"),
                    "safe-launch-terminal" => emit_tray_launch(app, "terminal"),
                    "safe-launch-browser" => emit_tray_launch(app, "browser"),
                    "safe-launch-other" => emit_tray_launch(app, "other"),
                    "copy-environment" => {
                        if let Err(error) = copy_proxy_environment(app) {
                            eprintln!("failed to copy proxy environment from tray: {error}");
                        }
                    }
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
            let gateway_addresses = load_gateway_addresses(&app_handle);
            tauri::async_runtime::spawn_blocking(move || {
                if let Err(error) = spawn_core(
                    &app_handle,
                    app_handle.state::<CoreProcess>().inner(),
                    "127.0.0.1:17680",
                    &gateway_addresses.api_gateway,
                    &gateway_addresses.http_proxy,
                    gateway_addresses.allow_lan,
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
            get_gateway_settings,
            open_profile_directory,
            start_core,
            stop_core,
            restart_core,
            system_certificate_status,
            install_system_certificate,
            uninstall_system_certificate,
            launch_ai_client,
            launch_app_with_proxy,
            launch_preset_with_proxy,
            open_purchase_page,
            open_official_website,
            set_tray_locale
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

fn emit_tray_launch(app: &AppHandle, preset: &'static str) {
    if let Err(error) = app.emit("tray-safe-launch", preset) {
        eprintln!("failed to start protected launch from tray: {error}");
    }
}

fn copy_proxy_environment(app: &AppHandle) -> Result<(), String> {
    let addresses = load_gateway_addresses(app);
    let http_proxy = format!("http://{}", addresses.http_proxy);
    let socks_proxy = format!("socks5h://{}", addresses.http_proxy);
    let mut assignments = vec![
        ("HTTP_PROXY", http_proxy.as_str()),
        ("HTTPS_PROXY", http_proxy.as_str()),
        ("http_proxy", http_proxy.as_str()),
        ("https_proxy", http_proxy.as_str()),
        ("ALL_PROXY", socks_proxy.as_str()),
        ("all_proxy", socks_proxy.as_str()),
    ];
    let certificate = system_integration::certificate_path(app)?;
    let certificate_text = certificate.to_string_lossy();
    if certificate.is_file() {
        assignments.extend([
            ("NODE_EXTRA_CA_CERTS", certificate_text.as_ref()),
            ("SSL_CERT_FILE", certificate_text.as_ref()),
            ("REQUESTS_CA_BUNDLE", certificate_text.as_ref()),
            ("CURL_CA_BUNDLE", certificate_text.as_ref()),
        ]);
    }
    let environment = format_shell_environment(&assignments);
    copy_text_to_clipboard(&environment)
}

fn format_shell_environment(assignments: &[(&str, &str)]) -> String {
    format!(
        "export {}",
        assignments
            .iter()
            .map(|(key, value)| format!("{key}={}", posix_shell_quote(value)))
            .collect::<Vec<_>>()
            .join(" ")
    )
}

fn posix_shell_quote(value: &str) -> String {
    format!("'{}'", value.replace('\'', "'\"'\"'"))
}

#[cfg(target_os = "macos")]
fn copy_text_to_clipboard(value: &str) -> Result<(), String> {
    copy_text_with_command("/usr/bin/pbcopy", &[], value)
}

#[cfg(target_os = "windows")]
fn copy_text_to_clipboard(value: &str) -> Result<(), String> {
    copy_text_with_command(
        "powershell.exe",
        &[
            "-NoProfile",
            "-NonInteractive",
            "-Command",
            "Set-Clipboard -Value ([Console]::In.ReadToEnd())",
        ],
        value,
    )
}

#[cfg(target_os = "linux")]
fn copy_text_to_clipboard(value: &str) -> Result<(), String> {
    for (program, arguments) in [
        ("wl-copy", &[][..]),
        ("xclip", &["-selection", "clipboard"][..]),
        ("xsel", &["--clipboard", "--input"][..]),
    ] {
        if copy_text_with_command(program, arguments, value).is_ok() {
            return Ok(());
        }
    }
    Err("no supported clipboard command was found (wl-copy, xclip, or xsel)".into())
}

#[cfg(not(any(target_os = "macos", target_os = "windows", target_os = "linux")))]
fn copy_text_to_clipboard(_value: &str) -> Result<(), String> {
    Err("copying to the clipboard is not supported on this platform".into())
}

fn copy_text_with_command(program: &str, arguments: &[&str], value: &str) -> Result<(), String> {
    use std::process::Stdio;

    let mut child = std::process::Command::new(program)
        .args(arguments)
        .stdin(Stdio::piped())
        .stdout(Stdio::null())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|error| format!("start clipboard command: {error}"))?;
    child
        .stdin
        .take()
        .ok_or_else(|| "clipboard command stdin is unavailable".to_string())?
        .write_all(value.as_bytes())
        .map_err(|error| format!("write clipboard content: {error}"))?;
    let output = child
        .wait_with_output()
        .map_err(|error| format!("wait for clipboard command: {error}"))?;
    if output.status.success() {
        Ok(())
    } else {
        Err(format!(
            "clipboard command failed: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        ))
    }
}

#[cfg(test)]
mod tests {
    use super::{format_shell_environment, resolve_core_addresses};

    #[test]
    fn proxy_environment_is_shell_quoted() {
        assert_eq!(
            format_shell_environment(&[
                ("HTTP_PROXY", "http://127.0.0.1:17682"),
                ("SSL_CERT_FILE", "/tmp/Remask's CA.pem")
            ]),
            "export HTTP_PROXY='http://127.0.0.1:17682' SSL_CERT_FILE='/tmp/Remask'\"'\"'s CA.pem'"
        );
    }

    #[test]
    fn gateway_addresses_only_leave_loopback_when_lan_access_is_enabled() {
        let local = resolve_core_addresses(
            "127.0.0.1:17680",
            "127.0.0.1:17681",
            "127.0.0.1:17682",
            false,
        )
        .unwrap();
        assert!(local.iter().all(|address| address.ip().is_loopback()));

        let lan = resolve_core_addresses(
            "127.0.0.1:17680",
            "127.0.0.1:17681",
            "127.0.0.1:17682",
            true,
        )
        .unwrap();
        assert!(lan[0].ip().is_loopback());
        assert!(lan[1].ip().is_unspecified());
        assert!(lan[2].ip().is_unspecified());
    }

    #[test]
    fn gateway_configuration_rejects_external_addresses_and_duplicate_ports() {
        assert!(resolve_core_addresses(
            "127.0.0.1:17680",
            "192.168.1.10:17681",
            "127.0.0.1:17682",
            true,
        )
        .is_err());
        assert!(resolve_core_addresses(
            "127.0.0.1:17680",
            "127.0.0.1:17680",
            "127.0.0.1:17682",
            false,
        )
        .is_err());
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
