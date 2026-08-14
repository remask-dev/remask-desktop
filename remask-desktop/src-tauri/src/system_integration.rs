use std::net::SocketAddr;
use std::path::{Path, PathBuf};
use std::process::{Command, Output};

use base64::{engine::general_purpose::STANDARD, Engine as _};
use serde::Serialize;
use sha2::{Digest, Sha256};
use tauri::{AppHandle, Manager};

const CERTIFICATE_NAME: &str = "Remask Local Privacy Proxy";

#[derive(Clone, Debug, Serialize)]
pub struct CertificateTrustStatus {
    pub supported: bool,
    pub installed: bool,
    pub platform: &'static str,
}

pub fn certificate_path(app: &AppHandle) -> Result<PathBuf, String> {
    let home_dir = app.path().home_dir().map_err(|error| error.to_string())?;
    Ok(home_dir
        .join(".remask")
        .join("certificates")
        .join("remask-ca.pem"))
}

pub fn certificate_status(app: &AppHandle) -> Result<CertificateTrustStatus, String> {
    platform_certificate_status(&certificate_path(app)?)
}

pub fn install_certificate(app: &AppHandle) -> Result<CertificateTrustStatus, String> {
    let path = certificate_path(app)?;
    validate_certificate(&path)?;
    platform_install_certificate(&path)?;
    let status = platform_certificate_status(&path)?;
    if status.supported && !status.installed {
        return Err("the certificate installer completed, but the Remask CA is not trusted".into());
    }
    Ok(status)
}

pub fn launch_client(
    app: &AppHandle,
    client: &str,
    forward_proxy_address: &str,
) -> Result<(), String> {
    let executable = client_executable(client)?;
    let proxy_url = validated_proxy_url(forward_proxy_address)?;

    let path = certificate_path(app)?;
    validate_certificate(&path)?;
    platform_launch_client(executable, &proxy_url, &path)
}

fn client_executable(client: &str) -> Result<&'static str, String> {
    match client {
        "claude" => Ok("claude"),
        "codex" => Ok("codex"),
        _ => Err("unsupported client; expected claude or codex".into()),
    }
}

fn validated_proxy_url(forward_proxy_address: &str) -> Result<String, String> {
    let listen_address: SocketAddr = forward_proxy_address
        .parse()
        .map_err(|_| "forward proxy address must be an IP address and port")?;
    if !listen_address.ip().is_loopback() || listen_address.port() == 0 {
        return Err("forward proxy address must use a loopback IP and non-zero port".into());
    }

    Ok(format!("http://{listen_address}"))
}

fn validate_certificate(path: &Path) -> Result<(), String> {
    if !path.is_file() {
        return Err(format!(
            "Remask CA certificate was not found at {}",
            path.display()
        ));
    }
    certificate_fingerprint(path).map(|_| ())
}

fn certificate_fingerprint(path: &Path) -> Result<String, String> {
    let pem = std::fs::read_to_string(path).map_err(|error| error.to_string())?;
    let encoded: String = pem
        .lines()
        .map(str::trim)
        .filter(|line| !line.is_empty() && !line.starts_with("-----"))
        .collect();
    let der = STANDARD
        .decode(encoded)
        .map_err(|_| "Remask CA certificate contains invalid PEM data".to_string())?;
    if der.is_empty() {
        return Err("Remask CA certificate is empty".into());
    }
    Ok(Sha256::digest(der)
        .iter()
        .map(|byte| format!("{byte:02X}"))
        .collect())
}

fn command_error(action: &str, output: &Output) -> String {
    let stderr = String::from_utf8_lossy(&output.stderr).trim().to_string();
    let stdout = String::from_utf8_lossy(&output.stdout).trim().to_string();
    let detail = if !stderr.is_empty() { stderr } else { stdout };
    if detail.is_empty() {
        format!("{action} failed with status {}", output.status)
    } else {
        format!("{action} failed: {detail}")
    }
}

#[cfg(target_os = "macos")]
fn platform_certificate_status(path: &Path) -> Result<CertificateTrustStatus, String> {
    if !Path::new("/usr/bin/security").is_file() {
        return Ok(CertificateTrustStatus {
            supported: false,
            installed: false,
            platform: "macos",
        });
    }
    if !path.is_file() {
        return Ok(CertificateTrustStatus {
            supported: true,
            installed: false,
            platform: "macos",
        });
    }
    let fingerprint = certificate_fingerprint(path)?;
    let keychain = macos_login_keychain(path)?;
    let output = Command::new("/usr/bin/security")
        .args(["find-certificate", "-a", "-Z", "-c", CERTIFICATE_NAME])
        .arg(keychain)
        .output()
        .map_err(|error| error.to_string())?;
    let listing = format!(
        "{}\n{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    )
    .replace([':', ' ', '\r', '\n'], "")
    .to_uppercase();
    Ok(CertificateTrustStatus {
        supported: true,
        installed: output.status.success() && listing.contains(&fingerprint),
        platform: "macos",
    })
}

#[cfg(target_os = "macos")]
fn platform_install_certificate(path: &Path) -> Result<(), String> {
    let keychain = macos_login_keychain(path)?;
    let output = Command::new("/usr/bin/security")
        .args(["add-trusted-cert", "-r", "trustRoot", "-k"])
        .arg(keychain)
        .arg(path)
        .output()
        .map_err(|error| error.to_string())?;
    if !output.status.success() {
        return Err(command_error("install system certificate", &output));
    }
    Ok(())
}

#[cfg(target_os = "macos")]
fn macos_login_keychain(certificate: &Path) -> Result<PathBuf, String> {
    let home_dir = certificate
        .ancestors()
        .nth(3)
        .ok_or_else(|| "cannot resolve the user home directory from the CA path".to_string())?;
    Ok(home_dir
        .join("Library")
        .join("Keychains")
        .join("login.keychain-db"))
}

#[cfg(target_os = "macos")]
fn platform_launch_client(
    executable: &str,
    proxy_url: &str,
    certificate: &Path,
) -> Result<(), String> {
    use std::io::Write;
    use std::os::unix::fs::PermissionsExt;

    let remask_dir = certificate
        .ancestors()
        .nth(2)
        .ok_or_else(|| "cannot resolve the Remask data directory from the CA path".to_string())?;
    let launchers_dir = remask_dir.join("launchers");
    std::fs::create_dir_all(&launchers_dir).map_err(|error| error.to_string())?;
    let launcher_path = launchers_dir.join(format!("{executable}.command"));
    let nonce = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map_err(|error| error.to_string())?
        .as_nanos();
    let temporary_path = launchers_dir.join(format!(
        ".{executable}.command.{}.{nonce}.tmp",
        std::process::id(),
    ));
    let mut temporary = std::fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .open(&temporary_path)
        .map_err(|error| error.to_string())?;
    let write_result = temporary
        .write_all(macos_launcher_script(executable, proxy_url, certificate).as_bytes())
        .and_then(|_| temporary.sync_all())
        .and_then(|_| {
            std::fs::set_permissions(&temporary_path, std::fs::Permissions::from_mode(0o700))
        })
        .and_then(|_| std::fs::rename(&temporary_path, &launcher_path));
    if let Err(error) = write_result {
        let _ = std::fs::remove_file(&temporary_path);
        return Err(error.to_string());
    }

    let output = Command::new("/usr/bin/open")
        .args(["-a", "Terminal"])
        .arg(&launcher_path)
        .output()
        .map_err(|error| error.to_string())?;
    if !output.status.success() {
        return Err(command_error("launch client terminal", &output));
    }
    Ok(())
}

#[cfg(target_os = "macos")]
fn macos_launcher_script(executable: &str, proxy_url: &str, certificate: &Path) -> String {
    let executable = posix_shell_quote(executable);
    let proxy_url = posix_shell_quote(proxy_url);
    let certificate = posix_shell_quote(&certificate.to_string_lossy());
    format!(
        "#!/bin/zsh\nexport HTTP_PROXY={proxy_url}\nexport HTTPS_PROXY={proxy_url}\nexport http_proxy={proxy_url}\nexport https_proxy={proxy_url}\nexport NODE_EXTRA_CA_CERTS={certificate}\nexport SSL_CERT_FILE={certificate}\nexport NO_PROXY='127.0.0.1,localhost'\nexport no_proxy='127.0.0.1,localhost'\nif command -v {executable} >/dev/null 2>&1; then\n  exec {executable}\nelse\n  echo 'Remask: client executable was not found in PATH:' {executable}\n  exec /bin/zsh -l\nfi\n"
    )
}

#[cfg(target_os = "macos")]
fn posix_shell_quote(value: &str) -> String {
    format!("'{}'", value.replace('\'', "'\"'\"'"))
}

#[cfg(target_os = "windows")]
fn platform_certificate_status(path: &Path) -> Result<CertificateTrustStatus, String> {
    if !path.is_file() {
        return Ok(CertificateTrustStatus {
            supported: true,
            installed: false,
            platform: "windows",
        });
    }
    let output = Command::new("certutil.exe")
        .args(["-user", "-verify"])
        .arg(path)
        .output()
        .map_err(|error| error.to_string())?;
    Ok(CertificateTrustStatus {
        supported: true,
        installed: output.status.success(),
        platform: "windows",
    })
}

#[cfg(target_os = "windows")]
fn platform_install_certificate(path: &Path) -> Result<(), String> {
    // The current-user Root store is shared by Windows TLS clients and avoids
    // weakening trust for other machine users.
    let output = Command::new("certutil.exe")
        .args(["-user", "-addstore", "-f", "Root"])
        .arg(path)
        .output()
        .map_err(|error| error.to_string())?;
    if !output.status.success() {
        return Err(command_error("install Windows certificate", &output));
    }
    Ok(())
}

#[cfg(target_os = "windows")]
fn platform_launch_client(
    executable: &str,
    proxy_url: &str,
    certificate: &Path,
) -> Result<(), String> {
    let status = Command::new("cmd.exe")
        .args(["/C", "start", "", "cmd.exe", "/K", executable])
        .env("HTTP_PROXY", proxy_url)
        .env("HTTPS_PROXY", proxy_url)
        .env("http_proxy", proxy_url)
        .env("https_proxy", proxy_url)
        .env("NODE_EXTRA_CA_CERTS", certificate)
        .env("SSL_CERT_FILE", certificate)
        .env("NO_PROXY", "127.0.0.1,localhost")
        .env("no_proxy", "127.0.0.1,localhost")
        .status()
        .map_err(|error| error.to_string())?;
    if !status.success() {
        return Err(format!(
            "launch client terminal failed with status {status}"
        ));
    }
    Ok(())
}

#[cfg(target_os = "linux")]
fn platform_certificate_status(path: &Path) -> Result<CertificateTrustStatus, String> {
    let supported = Path::new("/usr/sbin/update-ca-certificates").is_file()
        && Path::new("/usr/bin/pkexec").is_file();
    let target = Path::new("/usr/local/share/ca-certificates/remask-local-privacy-proxy.crt");
    let installed =
        supported && path.is_file() && std::fs::read(path).ok() == std::fs::read(target).ok();
    Ok(CertificateTrustStatus {
        supported,
        installed,
        platform: "linux",
    })
}

#[cfg(target_os = "linux")]
fn platform_install_certificate(path: &Path) -> Result<(), String> {
    if !Path::new("/usr/sbin/update-ca-certificates").is_file()
        || !Path::new("/usr/bin/pkexec").is_file()
    {
        return Err(
            "system certificate installation requires pkexec and update-ca-certificates".into(),
        );
    }
    let target = "/usr/local/share/ca-certificates/remask-local-privacy-proxy.crt";
    let copy = Command::new("/usr/bin/pkexec")
        .args(["/usr/bin/install", "-D", "-m", "0644"])
        .arg(path)
        .arg(target)
        .output()
        .map_err(|error| error.to_string())?;
    if !copy.status.success() {
        return Err(command_error("copy system certificate", &copy));
    }
    let update = Command::new("/usr/bin/pkexec")
        .arg("/usr/sbin/update-ca-certificates")
        .output()
        .map_err(|error| error.to_string())?;
    if !update.status.success() {
        return Err(command_error("update system certificates", &update));
    }
    Ok(())
}

#[cfg(target_os = "linux")]
fn platform_launch_client(
    executable: &str,
    proxy_url: &str,
    certificate: &Path,
) -> Result<(), String> {
    let candidates: &[(&str, &[&str])] = &[
        ("/usr/bin/x-terminal-emulator", &["-e"]),
        ("/usr/bin/gnome-terminal", &["--"]),
        ("/usr/bin/konsole", &["-e"]),
        ("/usr/bin/xterm", &["-e"]),
    ];
    let Some((terminal, arguments)) = candidates
        .iter()
        .find(|(path, _)| Path::new(path).is_file())
    else {
        return Err("no supported terminal emulator was found".into());
    };
    Command::new(terminal)
        .args(*arguments)
        .arg(executable)
        .env("HTTP_PROXY", proxy_url)
        .env("HTTPS_PROXY", proxy_url)
        .env("http_proxy", proxy_url)
        .env("https_proxy", proxy_url)
        .env("NODE_EXTRA_CA_CERTS", certificate)
        .env("SSL_CERT_FILE", certificate)
        .env("NO_PROXY", "127.0.0.1,localhost")
        .env("no_proxy", "127.0.0.1,localhost")
        .spawn()
        .map_err(|error| error.to_string())?;
    Ok(())
}

#[cfg(not(any(target_os = "macos", target_os = "windows", target_os = "linux")))]
fn platform_certificate_status(_path: &Path) -> Result<CertificateTrustStatus, String> {
    Ok(CertificateTrustStatus {
        supported: false,
        installed: false,
        platform: "unsupported",
    })
}

#[cfg(not(any(target_os = "macos", target_os = "windows", target_os = "linux")))]
fn platform_install_certificate(_path: &Path) -> Result<(), String> {
    Err("system certificate installation is not supported on this platform".into())
}

#[cfg(not(any(target_os = "macos", target_os = "windows", target_os = "linux")))]
fn platform_launch_client(
    _executable: &str,
    _proxy_url: &str,
    _certificate: &Path,
) -> Result<(), String> {
    Err("client quick launch is not supported on this platform".into())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rejects_remote_forward_proxy_addresses() {
        assert!(validated_proxy_url("192.0.2.10:17682").is_err());
        assert!(validated_proxy_url("127.0.0.1:0").is_err());
        assert_eq!(
            validated_proxy_url("127.0.0.1:17682").unwrap(),
            "http://127.0.0.1:17682"
        );
    }

    #[test]
    fn only_known_clients_are_mapped() {
        assert_eq!(client_executable("claude").unwrap(), "claude");
        assert_eq!(client_executable("codex").unwrap(), "codex");
        assert!(client_executable("sh").is_err());
    }

    #[cfg(target_os = "macos")]
    #[test]
    fn macos_launcher_quotes_shell_values() {
        assert_eq!(posix_shell_quote("plain"), "'plain'");
        assert_eq!(posix_shell_quote("user's path"), "'user'\"'\"'s path'");
        let script = macos_launcher_script(
            "claude",
            "http://127.0.0.1:17682",
            Path::new("/Users/test user/remask-ca.pem"),
        );
        assert!(script.contains("exec 'claude'"));
        assert!(script.contains("'/Users/test user/remask-ca.pem'"));
    }
}
