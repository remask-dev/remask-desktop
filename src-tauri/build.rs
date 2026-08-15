fn main() {
    // `tauri::include_image!` embeds the tray icon at compile time. Keep the
    // dev build in sync when the generated PNG or its SVG source changes.
    println!("cargo:rerun-if-changed=icons/tray-icon.png");
    println!("cargo:rerun-if-changed=tray-icon.svg");
    tauri_build::build()
}
