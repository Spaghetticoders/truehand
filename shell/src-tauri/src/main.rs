#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

mod allow;
mod anchor;
mod approver;
mod revoke;
mod server;
mod text;

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;
use std::time::Duration;

use serde::Serialize;
use tauri::ipc::CapabilityBuilder;
use tauri::menu::{CheckMenuItem, Menu, MenuItem, PredefinedMenuItem};
use tauri::tray::TrayIconBuilder;
use tauri::webview::Cookie;
use tauri::{AppHandle, Manager, RunEvent, State, Url, WebviewUrl, WebviewWindow, WebviewWindowBuilder, WindowEvent};
use tauri_plugin_autostart::{MacosLauncher, ManagerExt};
use tauri_plugin_dialog::{DialogExt, MessageDialogButtons, MessageDialogResult};

use server::Creds;
use text::Text;

const AUTOSTART_ARG: &str = "--autostart";

struct Shell {
    creds: Mutex<Option<Creds>>,
    text: &'static Text,
    quitting: AtomicBool,
    signing: AtomicBool,
}

fn creds(s: &Shell) -> Result<Creds, String> {
    s.creds.lock().unwrap().clone().ok_or_else(|| "Truehand server not started".into())
}

fn origin(url: &Url) -> String {
    url.origin().ascii_serialization()
}

/// Starts or reuses the server and signs the web view in with a cookie, so the token never
/// reaches JavaScript.
#[tauri::command]
async fn start(app: AppHandle, window: WebviewWindow, shell: State<'_, Shell>) -> Result<String, String> {
    let t = shell.text;
    let app0 = app.clone();
    let c = tauri::async_runtime::spawn_blocking(move || server::ensure(t, mac_pem(&app0))).await.map_err(|e| e.to_string())??;
    let (c2, app2) = (c.clone(), app.clone());
    let login = tauri::async_runtime::spawn_blocking(move || {
        register_key(&app2, &c2);
        server::request(&c2, "POST", "/api/login", None, false, Duration::from_secs(10))
    })
        .await
        .map_err(|e| e.to_string())??;
    if login.status != 204 && login.status != 200 {
        return Err(format!("login: HTTP {} {}", login.status, login.body));
    }
    let url = Url::parse(&c.url).map_err(|e| e.to_string())?;
    let host = url.host_str().unwrap_or("127.0.0.1").to_string();
    for raw in login.cookies {
        let mut cookie = Cookie::parse(raw).map_err(|e| e.to_string())?;
        if cookie.domain().is_none() {
            cookie.set_domain(host.clone());
        }
        window.set_cookie(cookie).map_err(|e| e.to_string())?;
    }
    app.add_capability(
        CapabilityBuilder::new("server")
            .remote(format!("{}/*", origin(&url)))
            .window("main")
            .permission("allow-start")
            .permission("allow-shell-request")
            .permission("allow-pick-folder")
            .permission("allow-sign-intent")
            .permission("allow-approver-public-key")
            .permission("allow-approver-key-fingerprint")
            .permission("allow-anchor-passkey")
            .permission("allow-revoke-owner"),
    )
    .map_err(|e| e.to_string())?;
    *shell.creds.lock().unwrap() = Some(c);
    Ok(format!("{}/", origin(&url)))
}

#[derive(Serialize)]
struct ShellReply {
    status: u16,
    body: serde_json::Value,
}

/// Makes one of the requests that need a person at the Mac, adding the shell's secret.
#[tauri::command]
async fn shell_request(app: AppHandle, shell: State<'_, Shell>, method: String, path: String, body: Option<serde_json::Value>) -> Result<ShellReply, String> {
    if !allow::human_request(&method, &path) {
        return Err(format!("not allowed through the shell: {method} {path}"));
    }
    let c = creds(&shell)?;
    let (m, p) = (method.clone(), path.clone());
    let c2 = c.clone();
    let r = tauri::async_runtime::spawn_blocking(move || {
        let body = with_mac_key(&app, &c2, &m, &p, body);
        server::request(&c2, &m, &p, body.as_ref(), true, Duration::from_secs(15 * 60))
    })
        .await
        .map_err(|e| e.to_string())??;
    let mut json: serde_json::Value = if r.body.trim().is_empty() { serde_json::Value::Null } else { serde_json::from_str(&r.body).unwrap_or(serde_json::Value::String(r.body)) };
    if path == allow::ROTATE && r.status == 200 {
        let token = json.get("token").and_then(|v| v.as_str()).ok_or("rotate: no token in reply")?.to_string();
        let mut c = c;
        server::update_token(&mut c, token)?;
        *shell.creds.lock().unwrap() = Some(c);
        json = serde_json::json!({});
    }
    Ok(ShellReply { status: r.status, body: json })
}

/// Adds this Mac's approver public key (only if it already exists) to the requests whose OS
/// dialog then labels each approver as this Mac's key or a foreign one.
fn with_mac_key(app: &AppHandle, c: &Creds, method: &str, path: &str, body: Option<serde_json::Value>) -> Option<serde_json::Value> {
    let Some(target) = allow::mac_key_target(method, path) else { return body };
    if let allow::MacKeyTarget::Confirm(qid) = target {
        let kind = server::request(c, "GET", "/api/queued", None, false, Duration::from_secs(10))
            .ok()
            .and_then(|r| serde_json::from_str::<serde_json::Value>(&r.body).ok())
            .and_then(|v| v.as_array()?.iter().find(|q| q["id"] == qid).map(|q| q["kind"].clone()));
        if kind.as_ref().and_then(|k| k.as_str()) != Some("commit-config") {
            return body;
        }
    }
    let Some(pem) = approver_key_path(app)
        .ok()
        .and_then(|p| approver::SecureEnclaveKey::load(&p))
        .and_then(|k| k.public_x963().ok())
        .and_then(|x| approver::public_pem(&x).ok())
    else {
        return body;
    };
    let mut obj = match body {
        None => serde_json::Map::new(),
        Some(serde_json::Value::Object(o)) => o,
        other => return other,
    };
    obj.insert("thisMacKey".into(), serde_json::Value::String(pem));
    Some(serde_json::Value::Object(obj))
}

#[tauri::command]
async fn pick_folder(app: AppHandle, shell: State<'_, Shell>) -> Result<Option<String>, String> {
    let title = shell.text.choose_folder;
    let picked = tauri::async_runtime::spawn_blocking(move || app.dialog().file().set_title(title).blocking_pick_folder())
        .await
        .map_err(|e| e.to_string())?;
    Ok(picked.and_then(|p| p.into_path().ok()).map(|p| p.to_string_lossy().into_owned()))
}

fn approver_key_path(app: &AppHandle) -> Result<std::path::PathBuf, String> {
    Ok(app.path().app_data_dir().map_err(|e| e.to_string())?.join("approver-se.key"))
}

/// Gives the server this Mac's approver public key, which it keeps in memory to check
/// intents forwarded to remote machines. Only an existing key; failures only cost remote PRs.
fn mac_pem(app: &AppHandle) -> Option<String> {
    approver_key_path(app)
        .ok()
        .and_then(|p| approver::SecureEnclaveKey::load(&p))
        .and_then(|k| k.public_x963().ok())
        .and_then(|x| approver::public_pem(&x).ok())
}

fn register_key(app: &AppHandle, c: &Creds) {
    if let Some(pem) = mac_pem(app) {
        let _ = server::request(c, "POST", "/api/shell/key", Some(&serde_json::json!({ "publicKey": pem })), true, Duration::from_secs(10));
    }
}

/// Gives the server the passkeys this Mac revoked for good after a revocation: the whole
/// list, it replaces the server's. A new server gets it on stdin (server::app_line).
fn send_revoked(c: &Creds) -> Result<(), String> {
    let list = server::load_revoked()?;
    let r = server::request(c, "POST", "/api/shell/revoked", Some(&serde_json::json!({ "revoked": list })), true, Duration::from_secs(10))?;
    if r.status != 204 && r.status != 200 {
        return Err(format!("revoked passkeys: HTTP {} {}", r.status, r.body));
    }
    Ok(())
}

fn approver_key(app: &AppHandle) -> Result<approver::SecureEnclaveKey, String> {
    approver::SecureEnclaveKey::load_or_create(&approver_key_path(app)?)
}

#[derive(serde::Deserialize)]
struct Prepared {
    intent: approver::Intent,
    message: String,
    #[serde(default)]
    title: String,
    #[serde(default)]
    branch: String,
}

/// Clears the signing flag however sign_intent ends.
struct SignGuard<'a>(&'a AtomicBool);
impl Drop for SignGuard<'_> {
    fn drop(&mut self) {
        self.0.store(false, Ordering::SeqCst);
    }
}

/// Signs, with the Secure Enclave key behind Touch ID, an intent the verified server
/// prepared for this task and action: the page only says which. One signature at a time.
#[tauri::command]
async fn sign_intent(app: AppHandle, shell: State<'_, Shell>, project_id: String, task_id: String, action: String, approver: String) -> Result<approver::Signed, String> {
    let t = shell.text;
    if shell.signing.swap(true, Ordering::SeqCst) {
        return Err(t.signing_busy.into());
    }
    let _guard = SignGuard(&shell.signing);
    if !allow::segment_ok(&project_id) || !allow::segment_ok(&task_id) || !["approve-plan", "push", "open-pr"].contains(&action.as_str()) {
        return Err("sign_intent: invalid request".into());
    }
    let c = creds(&shell)?;
    let path = format!("/api/projects/{project_id}/tasks/{task_id}/intent");
    let body = serde_json::json!({ "action": action, "approver": approver });
    let r = tauri::async_runtime::spawn_blocking(move || server::request(&c, "POST", &path, Some(&body), false, Duration::from_secs(30)))
        .await
        .map_err(|e| e.to_string())??;
    if r.status != 200 {
        return Err(format!("intent: HTTP {} {}", r.status, r.body));
    }
    let p: Prepared = serde_json::from_str(&r.body).map_err(|e| format!("intent: {e}"))?;
    let intent = p.intent;
    if intent.action != action || intent.task != task_id || intent.approver != approver {
        return Err("intent: the server prepared a different action, task or approver".into());
    }
    let msg = approver::message(&intent)?;
    if approver::base64_std(&msg) != p.message {
        return Err("intent: the server's message differs from the intent".into());
    }
    let clean = |s: &str| s.chars().filter(|c| !c.is_control()).take(80).collect::<String>();
    let (template, sha) = match intent.action.as_str() {
        "approve-plan" => (t.sign_approve_plan, intent.plan_sha256.as_str()),
        "push" => (t.sign_push, intent.head.as_str()),
        _ => (t.sign_open_pr, intent.head.as_str()),
    };
    let reason = template
        .replace("{task}", &intent.task)
        .replace("{title}", &clean(&p.title))
        .replace("{repo}", &intent.repo)
        .replace("{branch}", &clean(&p.branch))
        .replace("{sha}", sha.get(..12).unwrap_or(sha));
    let der = tauri::async_runtime::spawn_blocking(move || approver_key(&app)?.sign(&msg, &reason))
        .await
        .map_err(|e| e.to_string())??;
    Ok(approver::Signed { intent, format: "es256", sig: approver::base64_std(&der) })
}

fn get_json(c: &Creds, path: &str, shell: bool) -> Result<serde_json::Value, String> {
    let r = server::request(c, "GET", path, None, shell, Duration::from_secs(30))?;
    if r.status != 200 {
        return Err(format!("{path}: HTTP {} {}", r.status, r.body));
    }
    serde_json::from_str(&r.body).map_err(|e| format!("{path}: {e}"))
}

/// Makes a passkey waiting for this Mac an owner of the server: signs, with the Secure
/// Enclave key behind Touch ID, the anchor-owner intent the verified server prepared for it,
/// once it matches that passkey, the host the server serves passkeys on and the clock. The
/// page only says which passkey. One signature at a time, shared with sign_intent.
#[tauri::command]
async fn anchor_passkey(app: AppHandle, shell: State<'_, Shell>, id: String) -> Result<(), String> {
    let t = shell.text;
    if shell.signing.swap(true, Ordering::SeqCst) {
        return Err(t.signing_busy.into());
    }
    let _guard = SignGuard(&shell.signing);
    if !allow::segment_ok(&id) || !anchor::credential_ok(&id) {
        return Err("anchor_passkey: invalid request".into());
    }
    let c = creds(&shell)?;
    let (c2, id2) = (c.clone(), id.clone());
    let (ready, reason) = tauri::async_runtime::spawn_blocking(move || -> Result<_, String> {
        let prepared = get_json(&c2, &format!("/api/passkeys/{id2}/anchor-intent"), true)?;
        let passkeys = get_json(&c2, "/api/passkeys", false)?;
        let settings = get_json(&c2, "/api/settings", false)?;
        let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map_err(|e| e.to_string())?.as_secs() as i64;
        let ready = anchor::check_replies(&id2, prepared, &passkeys, &settings, now)?;
        let clean = |s: &str| s.chars().filter(|c| !c.is_control()).take(40).collect::<String>();
        let reason = t
            .sign_anchor_owner
            .replace("{label}", &clean(&ready.checked.label))
            .replace("{device}", &ready.device.as_deref().map(clean).unwrap_or_else(|| t.unknown_device.into()))
            .replace("{key}", &anchor::grouped(&ready.checked.key))
            .replace("{server}", &ready.intent.server);
        Ok((ready, reason))
    })
    .await
    .map_err(|e| e.to_string())??;
    let app2 = app.clone();
    let msg = ready.checked.message;
    let der = tauri::async_runtime::spawn_blocking(move || approver_key(&app2)?.sign(&msg, &reason))
        .await
        .map_err(|e| e.to_string())??;
    let signed = anchor::Signed { intent: ready.intent, format: "es256", sig: approver::base64_std(&der) };
    let body = serde_json::json!({ "signed": signed });
    let r = tauri::async_runtime::spawn_blocking(move || {
        // The key may have been created just now, and the server checks the grant with it.
        register_key(&app, &c);
        server::request(&c, "POST", &format!("/api/passkeys/{id}/anchor"), Some(&body), true, Duration::from_secs(30))
    })
    .await
    .map_err(|e| e.to_string())??;
    if r.status != 204 && r.status != 200 {
        return Err(format!("anchor: HTTP {} {}", r.status, r.body));
    }
    Ok(())
}

/// Revokes passkey `passkey_id` for good on this Mac, after Touch ID: adds it to the list in the
/// Keychain, gives the server the list, then removes the passkey from the server's file if it
/// is still there. Works on a listed passkey or on a soft revocation. One signature at a time.
#[tauri::command]
async fn revoke_owner(app: AppHandle, shell: State<'_, Shell>, passkey_id: String) -> Result<(), String> {
    let id = passkey_id;
    let t = shell.text;
    if shell.signing.swap(true, Ordering::SeqCst) {
        return Err(t.signing_busy.into());
    }
    let _guard = SignGuard(&shell.signing);
    if !allow::segment_ok(&id) || !anchor::credential_ok(&id) {
        return Err("revoke_owner: invalid request".into());
    }
    let c = creds(&shell)?;
    tauri::async_runtime::spawn_blocking(move || {
        let passkeys = get_json(&c, "/api/passkeys", false)?;
        let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map_err(|e| e.to_string())?.as_secs() as i64;
        let target = revoke::target(&id, &passkeys, &revoke::rfc3339(now))?;
        let clean = |s: &str| s.chars().filter(|c| !c.is_control()).take(40).collect::<String>();
        let reason = t.sign_revoke_owner.replace("{label}", &clean(&target.revocation.label)).replace("{key}", &anchor::grouped(&target.key));
        approver_key(&app)?.sign(&revoke::message(&target.revocation), &reason)?;
        server::save_revoked(&revoke::add(server::load_revoked()?, target.revocation))?;
        send_revoked(&c)?;
        if target.delete {
            let r = server::request(&c, "DELETE", &format!("/api/passkeys/{id}"), None, true, Duration::from_secs(15 * 60))?;
            if r.status >= 300 && r.status != 404 {
                return Err(format!("revoked on this Mac; removing it from the server: HTTP {} {}", r.status, r.body));
            }
        }
        Ok(())
    })
    .await
    .map_err(|e| e.to_string())?
}

#[derive(Serialize)]
struct ApproverKey {
    pem: String,
    key: String,
}

/// The approver's public key, for `approvers` in a repo's .truehand/config.yaml, and its
/// fingerprint as the server shows it next to each approver.
#[tauri::command]
async fn approver_public_key(app: AppHandle, shell: State<'_, Shell>) -> Result<ApproverKey, String> {
    let c = shell.creds.lock().unwrap().clone();
    tauri::async_runtime::spawn_blocking(move || {
        let x963 = approver_key(&app)?.public_x963()?;
        if let Some(c) = c {
            register_key(&app, &c);
        }
        Ok(ApproverKey { pem: approver::public_pem(&x963)?, key: approver::fingerprint(&x963)? })
    })
    .await
    .map_err(|e| e.to_string())?
}

/// The fingerprint of this Mac's approver key, or null while it has none: never creates the
/// key, and reading the public half needs no Touch ID.
#[tauri::command]
async fn approver_key_fingerprint(app: AppHandle) -> Result<Option<String>, String> {
    tauri::async_runtime::spawn_blocking(move || match approver::SecureEnclaveKey::load(&approver_key_path(&app)?) {
        Some(k) => approver::fingerprint(&k.public_x963()?).map(Some),
        None => Ok(None),
    })
    .await
    .map_err(|e| e.to_string())?
}

fn show(app: &AppHandle) {
    if let Some(w) = app.get_webview_window("main") {
        let _ = w.show();
        let _ = w.unminimize();
        let _ = w.set_focus();
    }
}

fn stop_server(c: &Creds) {
    if let Ok(r) = server::request(c, "POST", "/api/shutdown", None, true, Duration::from_secs(10)) {
        if r.status < 300 {
            return;
        }
    }
    // Only the pid the server announced when it was ready, and only while its command and
    // start time are still the ones recorded then.
    if !c.started.is_empty() && server::process_identity(c.pid).as_deref() == Some(c.started.as_str()) {
        unsafe {
            libc::kill(c.pid, libc::SIGTERM);
        }
    }
}

fn ask_quit(app: &AppHandle) {
    let app = app.clone();
    std::thread::spawn(move || {
        let shell = app.state::<Shell>();
        let t = shell.text;
        let running = shell.creds.lock().unwrap().clone();
        if let Some(c) = running {
            let answer = app
                .dialog()
                .message(t.quit_body)
                .title(t.quit_title)
                .buttons(MessageDialogButtons::YesNoCancelCustom(t.stop_server.into(), t.keep_server.into(), t.cancel.into()))
                .blocking_show_with_result();
            match answer {
                MessageDialogResult::Yes => {
                    stop_server(&c);
                    server::forget_creds();
                }
                MessageDialogResult::Custom(ref s) if s == t.stop_server => {
                    stop_server(&c);
                    server::forget_creds();
                }
                MessageDialogResult::No => {}
                MessageDialogResult::Custom(ref s) if s == t.keep_server => {}
                _ => return,
            }
        }
        shell.quitting.store(true, Ordering::SeqCst);
        app.exit(0);
    });
}

fn tray(app: &AppHandle, t: &Text) -> tauri::Result<()> {
    let open = MenuItem::with_id(app, "open", t.open, true, None::<&str>)?;
    let at_login = app.autolaunch().is_enabled().unwrap_or(false);
    let login = CheckMenuItem::with_id(app, "login", t.launch_at_login, true, at_login, None::<&str>)?;
    let quit = MenuItem::with_id(app, "quit", t.quit, true, None::<&str>)?;
    let menu = Menu::with_items(app, &[&open, &PredefinedMenuItem::separator(app)?, &login, &PredefinedMenuItem::separator(app)?, &quit])?;
    let icon = tauri::image::Image::from_bytes(include_bytes!("../icons/tray.png"))?;
    TrayIconBuilder::with_id("main")
        .icon(icon)
        .icon_as_template(true)
        .tooltip("Truehand")
        .menu(&menu)
        .on_menu_event(move |app, ev| match ev.id().as_ref() {
            "open" => show(app),
            "login" => {
                let al = app.autolaunch();
                let _ = if al.is_enabled().unwrap_or(false) { al.disable() } else { al.enable() };
                let _ = login.set_checked(al.is_enabled().unwrap_or(false));
            }
            "quit" => ask_quit(app),
            _ => {}
        })
        .build(app)?;
    Ok(())
}

/// The window only shows the bundled start page and the server it started; anything else
/// (a PR link, a ticket) opens in the default browser.
fn navigation_allowed(app: &AppHandle, url: &Url) -> bool {
    if url.scheme() == "tauri" || url.host_str() == Some("tauri.localhost") {
        return true;
    }
    let server = app.state::<Shell>().creds.lock().unwrap().as_ref().and_then(|c| Url::parse(&c.url).ok());
    if let Some(s) = server {
        if origin(&s) == origin(url) {
            return true;
        }
    }
    if url.scheme() == "http" || url.scheme() == "https" {
        let _ = std::process::Command::new("/usr/bin/open").arg(url.as_str()).spawn();
    }
    false
}

fn main() {
    let t = text::current();
    let hidden = std::env::args().any(|a| a == AUTOSTART_ARG);
    let app = tauri::Builder::default()
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_autostart::init(MacosLauncher::LaunchAgent, Some(vec![AUTOSTART_ARG])))
        .manage(Shell { creds: Mutex::new(None), text: t, quitting: AtomicBool::new(false), signing: AtomicBool::new(false) })
        .invoke_handler(tauri::generate_handler![start, shell_request, pick_folder, sign_intent, approver_public_key, approver_key_fingerprint, anchor_passkey, revoke_owner])
        .setup(move |app| {
            let handle = app.handle().clone();
            WebviewWindowBuilder::new(app, "main", WebviewUrl::App("index.html".into()))
                .title("Truehand")
                .inner_size(1280.0, 820.0)
                .min_inner_size(380.0, 560.0)
                .visible(!hidden)
                .on_navigation(move |url| navigation_allowed(&handle, url))
                .build()?;
            tray(app.handle(), t)?;
            Ok(())
        })
        .on_window_event(|window, event| {
            // Closing the window keeps the app (and the server) in the tray.
            if let WindowEvent::CloseRequested { api, .. } = event {
                api.prevent_close();
                let _ = window.hide();
            }
        })
        .build(tauri::generate_context!())
        .expect("error while building Truehand");
    app.run(|app, event| match event {
        RunEvent::ExitRequested { api, code, .. } => {
            if code.is_none() && !app.state::<Shell>().quitting.load(Ordering::SeqCst) {
                api.prevent_exit();
                ask_quit(app);
            }
        }
        RunEvent::Reopen { .. } => show(app),
        _ => {}
    });
}
