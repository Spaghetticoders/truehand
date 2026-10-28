use std::path::PathBuf;
use std::process::Command;

/// The Secure Enclave key lives behind CryptoKit, which only Swift can call: build it as a
/// static library and link the Swift runtime that macOS ships.
fn swift() {
    let out = PathBuf::from(std::env::var("OUT_DIR").unwrap());
    let lib = out.join("libthapprover.a");
    let target = format!("{}-apple-macosx12.0", if std::env::var("CARGO_CFG_TARGET_ARCH").unwrap() == "aarch64" { "arm64" } else { "x86_64" });
    let st = Command::new("xcrun")
        .args(["swiftc", "-emit-library", "-static", "-O", "-parse-as-library", "-module-name", "thapprover", "-target", &target, "-o"])
        .arg(&lib)
        .arg("swift/approver.swift")
        .status()
        .expect("xcrun swiftc");
    assert!(st.success(), "swiftc failed");
    let toolchain = String::from_utf8(Command::new("xcrun").args(["--find", "swiftc"]).output().unwrap().stdout).unwrap();
    let toolchain_lib = PathBuf::from(toolchain.trim()).parent().unwrap().parent().unwrap().join("lib/swift/macosx");
    println!("cargo:rerun-if-changed=swift/approver.swift");
    println!("cargo:rustc-link-search=native={}", out.display());
    println!("cargo:rustc-link-lib=static=thapprover");
    println!("cargo:rustc-link-search=native={}", toolchain_lib.display());
    println!("cargo:rustc-link-search=native=/usr/lib/swift");
    println!("cargo:rustc-link-arg=-Wl,-rpath,/usr/lib/swift");
    for f in ["CryptoKit", "LocalAuthentication", "Security", "Foundation"] {
        println!("cargo:rustc-link-lib=framework={f}");
    }
}

fn main() {
    swift();
    tauri_build::try_build(
        tauri_build::Attributes::new().app_manifest(tauri_build::AppManifest::new().commands(&[
            "start",
            "shell_request",
            "pick_folder",
            "sign_intent",
            "approver_public_key",
            "approver_key_fingerprint",
        ])),
    )
    .expect("tauri build");
}
