use std::fs::{self, OpenOptions};
use std::io::{BufRead, BufReader, Read, Write};
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};
use std::os::unix::process::CommandExt;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::mpsc;
use std::time::Duration;

use serde::{Deserialize, Serialize};

use crate::text::Text;

const SERVICE: &str = "it.spaghetticoders.truehand";
const DEFAULT_ADDR: &str = "127.0.0.1:7878";
pub const SHELL_HEADER: &str = "X-Truehand-Shell";

/// What the shell needs to talk to a server it started: kept only in the Keychain, so a
/// relaunched shell can reuse a server that outlived it.
#[derive(Clone, Serialize, Deserialize)]
pub struct Creds {
    pub token: String,
    pub shell_secret: String,
    pub url: String,
    pub pid: i32,
    /// `ps -o lstart=` of pid when it said it was ready: a reused pid will not match.
    #[serde(default)]
    pub started: String,
}

/// TRUEHAND_SHELL_HOME and TRUEHAND_SHELL_ADDR run the server on a scratch home and port,
/// with its own Keychain entry, so the shell can be tried without touching the real one.
struct Profile {
    home: Option<PathBuf>,
    addr: String,
    account: String,
}

fn profile() -> Profile {
    let home = std::env::var_os("TRUEHAND_SHELL_HOME").map(PathBuf::from);
    let addr = std::env::var("TRUEHAND_SHELL_ADDR").unwrap_or_else(|_| DEFAULT_ADDR.into());
    let account = match &home {
        Some(h) => format!("test:{}", h.display()),
        None => "app".into(),
    };
    Profile { home, addr, account }
}

fn load_creds(p: &Profile) -> Option<Creds> {
    let raw = security_framework::passwords::get_generic_password(SERVICE, &p.account).ok()?;
    serde_json::from_slice(&raw).ok()
}

fn save_creds(p: &Profile, c: &Creds) -> Result<(), String> {
    let raw = serde_json::to_vec(c).map_err(|e| e.to_string())?;
    security_framework::passwords::set_generic_password(SERVICE, &p.account, &raw).map_err(|e| e.to_string())
}

pub fn forget_creds() {
    let _ = security_framework::passwords::delete_generic_password(SERVICE, &profile().account);
}

pub fn update_token(c: &mut Creds, token: String) -> Result<(), String> {
    c.token = token;
    save_creds(&profile(), c)
}

pub struct Reply {
    pub status: u16,
    pub body: String,
    pub cookies: Vec<String>,
}

fn agent() -> ureq::Agent {
    ureq::Agent::config_builder().http_status_as_error(false).max_redirects(0).build().into()
}

fn send(agent: &ureq::Agent, c: &Creds, method: &str, path: &str, headers: &[(&str, &str)], body: Option<&serde_json::Value>, timeout: Duration) -> Result<Reply, ureq::Error> {
    let mut b = ureq::http::Request::builder().method(method).uri(format!("{}{}", c.url, path));
    for (k, v) in headers {
        b = b.header(*k, *v);
    }
    let payload = match body {
        Some(v) => {
            b = b.header("Content-Type", "application/json");
            serde_json::to_vec(v).expect("json")
        }
        None => Vec::new(),
    };
    let req = agent.configure_request(b.body(payload).expect("request")).timeout_global(Some(timeout)).build();
    let mut resp = agent.run(req)?;
    let cookies = resp
        .headers()
        .get_all("set-cookie")
        .iter()
        .filter_map(|v| v.to_str().ok().map(String::from))
        .collect();
    let status = resp.status().as_u16();
    let body = resp.body_mut().read_to_string().unwrap_or_default();
    Ok(Reply { status, body, cookies })
}

extern "C" {
    fn CC_SHA256(data: *const u8, len: u32, md: *mut u8) -> *mut u8;
    fn CCHmac(alg: u32, key: *const u8, key_len: usize, data: *const u8, data_len: usize, mac_out: *mut u8);
}
const CC_HMAC_SHA256: u32 = 2;
const CHALLENGE_PREFIX: &str = "truehand-shell-challenge/1\n";

pub fn sha256(data: &[u8]) -> [u8; 32] {
    let mut md = [0u8; 32];
    unsafe {
        CC_SHA256(data.as_ptr(), data.len() as u32, md.as_mut_ptr());
    }
    md
}

/// What only a server holding SHA-256(shell secret) can compute for this nonce.
pub fn challenge_mac(shell_secret: &str, nonce: &str) -> String {
    let key = sha256(shell_secret.as_bytes());
    let mut mac = [0u8; 32];
    let msg = format!("{CHALLENGE_PREFIX}{nonce}");
    unsafe {
        CCHmac(CC_HMAC_SHA256, key.as_ptr(), key.len(), msg.as_ptr(), msg.len(), mac.as_mut_ptr());
    }
    hex(&mac)
}

pub enum Identity {
    Ours,
    Nobody,
    Stranger,
}

/// Asks whoever listens at c.url to prove it knows the shell secret, without sending the
/// secret or the token: anything could have taken the port since the shell last looked.
pub fn identify(c: &Creds) -> Identity {
    identify_with(&agent(), c)
}

fn identify_with(agent: &ureq::Agent, c: &Creds) -> Identity {
    let nonce = secret();
    let body = serde_json::json!({ "nonce": nonce });
    match send(agent, c, "POST", "/api/shell/challenge", &[], Some(&body), Duration::from_secs(3)) {
        Err(_) => Identity::Nobody,
        Ok(r) if r.status == 200 => {
            let mac = serde_json::from_str::<serde_json::Value>(&r.body).ok().and_then(|v| v["mac"].as_str().map(String::from));
            match mac {
                Some(m) if constant_eq(m.as_bytes(), challenge_mac(&c.shell_secret, &nonce).as_bytes()) => Identity::Ours,
                _ => Identity::Stranger,
            }
        }
        Ok(_) => Identity::Stranger,
    }
}

fn constant_eq(a: &[u8], b: &[u8]) -> bool {
    a.len() == b.len() && a.iter().zip(b).fold(0u8, |acc, (x, y)| acc | (x ^ y)) == 0
}

/// Sends the token (and the shell secret when `shell`) only to the server that proved it
/// is the one this shell started, over the same kept-alive connection as the proof.
pub fn request(c: &Creds, method: &str, path: &str, body: Option<&serde_json::Value>, shell: bool, timeout: Duration) -> Result<Reply, String> {
    let agent = agent();
    if !matches!(identify_with(&agent, c), Identity::Ours) {
        return Err(crate::text::current().unrecognized.into());
    }
    let auth = format!("Bearer {}", c.token);
    let mut headers = vec![("Authorization", auth.as_str())];
    if shell {
        headers.push((SHELL_HEADER, c.shell_secret.as_str()));
    }
    send(&agent, c, method, path, &headers, body, timeout).map_err(|e| e.to_string())
}

/// Reuses the server whose credentials are in the Keychain if it proves it is that server
/// and still takes the token; starts a new one if nothing answers there.
pub fn ensure(t: &Text) -> Result<Creds, String> {
    let p = profile();
    if let Some(c) = load_creds(&p) {
        match identify(&c) {
            Identity::Ours => {
                if matches!(request(&c, "GET", "/api/me", None, false, Duration::from_secs(3)), Ok(r) if r.status == 200) {
                    return Ok(c);
                }
            }
            Identity::Stranger => return Err(t.unrecognized.into()),
            Identity::Nobody => {}
        }
    }
    let c = spawn(&p, t)?;
    if !matches!(identify(&c), Identity::Ours) {
        return Err(t.unrecognized.into());
    }
    save_creds(&p, &c)?;
    Ok(c)
}

pub fn hex(b: &[u8]) -> String {
    b.iter().map(|x| format!("{x:02x}")).collect()
}

fn secret() -> String {
    let mut b = [0u8; 32];
    getrandom::fill(&mut b).expect("random");
    hex(&b)
}

fn sidecar() -> Result<PathBuf, String> {
    let exe = std::env::current_exe().map_err(|e| e.to_string())?;
    Ok(exe.with_file_name("truehand"))
}

/// A GUI app gets launchd's bare PATH; claude, git and gh live where the login shell puts them.
fn user_path() -> String {
    let fallback = std::env::var("PATH").unwrap_or_default();
    let sh = std::env::var("SHELL").unwrap_or_else(|_| "/bin/zsh".into());
    let (tx, rx) = mpsc::channel();
    std::thread::spawn(move || {
        let out = Command::new(sh)
            .args(["-ilc", "printf '\\n__TH_PATH__%s__TH_PATH__' \"$PATH\""])
            .stdin(Stdio::null())
            .stderr(Stdio::null())
            .output();
        let _ = tx.send(out);
    });
    if let Ok(Ok(out)) = rx.recv_timeout(Duration::from_secs(5)) {
        let s = String::from_utf8_lossy(&out.stdout);
        if let Some(rest) = s.split("__TH_PATH__").nth(1) {
            if !rest.is_empty() {
                return rest.to_string();
            }
        }
    }
    format!("/opt/homebrew/bin:/usr/local/bin:{fallback}")
}

fn app_dir(home: &Path) -> Result<PathBuf, String> {
    let dir = home.join(".truehand").join("app");
    fs::DirBuilder::new().recursive(true).mode(0o700).create(&dir).map_err(|e| e.to_string())?;
    Ok(dir)
}

enum Event {
    Line(String),
    Exited(Option<i32>),
}

#[derive(Deserialize)]
struct Ready {
    ready: bool,
    url: String,
    pid: i32,
}

fn spawn(p: &Profile, t: &Text) -> Result<Creds, String> {
    let home = match &p.home {
        Some(h) => h.clone(),
        None => PathBuf::from(std::env::var_os("HOME").ok_or("HOME is not set")?),
    };
    let log_path = app_dir(&home)?.join("serve.log");
    let log = OpenOptions::new().create(true).append(true).mode(0o600).open(&log_path).map_err(|e| e.to_string())?;
    let log_out = log.try_clone().map_err(|e| e.to_string())?;

    let mut cmd = Command::new(sidecar()?);
    cmd.args(["serve", "--app", "--addr", &p.addr])
        .current_dir(&home)
        .env("HOME", &home)
        .env("PATH", user_path())
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(log);
    // Its own session: quitting the shell or closing its window never takes the agents down.
    unsafe {
        cmd.pre_exec(|| {
            if libc::setsid() == -1 {
                return Err(std::io::Error::last_os_error());
            }
            Ok(())
        });
    }
    let mut child = cmd.spawn().map_err(|e| format!("{}: {e}", t.cannot_start))?;

    let token = secret();
    let shell_secret = secret();
    let line = serde_json::json!({ "token": token, "shell_secret": shell_secret }).to_string() + "\n";
    if let Some(mut stdin) = child.stdin.take() {
        let _ = stdin.write_all(line.as_bytes());
    }

    let (tx, rx) = mpsc::channel();
    let stdout = child.stdout.take().expect("stdout");
    let tx_out = tx.clone();
    std::thread::spawn(move || {
        let mut r = BufReader::new(stdout);
        let mut first = String::new();
        let _ = r.read_line(&mut first);
        let _ = tx_out.send(Event::Line(first));
        let mut log = log_out;
        let mut buf = [0u8; 4096];
        while let Ok(n) = r.read(&mut buf) {
            if n == 0 {
                break;
            }
            let _ = log.write_all(&buf[..n]);
        }
    });
    std::thread::spawn(move || {
        let code = child.wait().ok().and_then(|s| s.code());
        let _ = tx.send(Event::Exited(code));
    });

    let fail = |why: String| -> String {
        let tail = fs::read_to_string(&log_path).unwrap_or_default();
        let last: Vec<&str> = tail.lines().rev().take(6).collect();
        let last: Vec<&str> = last.into_iter().rev().collect();
        let mut msg = why;
        if !last.is_empty() {
            msg.push_str("\n\n");
            msg.push_str(&last.join("\n"));
        }
        if tail.contains("already running") {
            msg = format!("{}\n\n{msg}", t.already_running);
        }
        msg
    };
    match rx.recv_timeout(Duration::from_secs(60)) {
        Ok(Event::Line(l)) if !l.trim().is_empty() => {
            let r: Ready = serde_json::from_str(l.trim()).map_err(|_| fail(format!("{}: {}", t.unexpected, l.trim())))?;
            if !r.ready {
                return Err(fail(t.unexpected.into()));
            }
            Ok(Creds { token, shell_secret, url: r.url, pid: r.pid, started: process_identity(r.pid).unwrap_or_default() })
        }
        Ok(Event::Exited(code)) => Err(fail(format!("{} ({})", t.exited, code.map_or("?".into(), |c| c.to_string())))),
        Ok(Event::Line(_)) => {
            std::thread::sleep(Duration::from_millis(200));
            Err(fail(t.exited.into()))
        }
        Err(_) => Err(fail(t.timeout.into())),
    }
}

/// Command name and start time of pid, the pair that tells a process apart from a later one
/// that reused its pid.
pub fn process_identity(pid: i32) -> Option<String> {
    let out = Command::new("/bin/ps").args(["-o", "comm=,lstart=", "-p", &pid.to_string()]).output().ok()?;
    let s = String::from_utf8_lossy(&out.stdout).trim().to_string();
    (!s.is_empty()).then_some(s)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Same construction the server uses (internal/serve): HMAC-SHA256 keyed with the raw
    /// SHA-256 of the secret. Reference value from Python's hmac/hashlib.
    #[test]
    fn challenge_mac_matches_reference() {
        let nonce = "00".repeat(32);
        assert_eq!(challenge_mac("s3cret", &nonce), "a3c4a924ce728a8fe665fa04adbc071e6ca391a450cf3cd1ee706c98743a3b67");
    }

    use std::io::{BufRead, BufReader, Read, Write};
    use std::net::TcpListener;
    use std::sync::{Arc, Mutex};

    #[derive(Clone, Copy)]
    enum Fake {
        Right,
        Wrong,
        NotFound,
    }

    /// A local HTTP server that answers the challenge as told, answers 200 to anything else,
    /// and records every request's head.
    fn fake(mode: Fake, secret: &'static str) -> (String, Arc<Mutex<Vec<String>>>) {
        let ln = TcpListener::bind("127.0.0.1:0").unwrap();
        let url = format!("http://{}", ln.local_addr().unwrap());
        let seen = Arc::new(Mutex::new(Vec::new()));
        let log = seen.clone();
        std::thread::spawn(move || {
            for conn in ln.incoming() {
                let Ok(conn) = conn else { return };
                let log = log.clone();
                log.lock().unwrap().push("CONNECT".into());
                std::thread::spawn(move || {
                    let mut r = BufReader::new(conn.try_clone().unwrap());
                    let mut w = conn;
                    loop {
                        let mut head = String::new();
                        let mut line = String::new();
                        while r.read_line(&mut line).unwrap_or(0) > 0 && line != "\r\n" {
                            head.push_str(&line);
                            line.clear();
                        }
                        if head.is_empty() {
                            return;
                        }
                        let len = head
                            .lines()
                            .find_map(|l| l.to_ascii_lowercase().strip_prefix("content-length:").map(|v| v.trim().parse::<usize>().unwrap()))
                            .unwrap_or(0);
                        let mut body = vec![0u8; len];
                        r.read_exact(&mut body).unwrap();
                        log.lock().unwrap().push(head.clone());
                        let (status, out) = if head.starts_with("POST /api/shell/challenge ") {
                            let nonce = serde_json::from_slice::<serde_json::Value>(&body).unwrap()["nonce"].as_str().unwrap().to_string();
                            match mode {
                                Fake::Right => ("200 OK", format!("{{\"mac\":\"{}\"}}", challenge_mac(secret, &nonce))),
                                Fake::Wrong => ("200 OK", format!("{{\"mac\":\"{}\"}}", challenge_mac("other", &nonce))),
                                Fake::NotFound => ("404 Not Found", "{}".to_string()),
                            }
                        } else {
                            ("200 OK", "{}".to_string())
                        };
                        let _ = write!(w, "HTTP/1.1 {status}\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n{out}", out.len());
                    }
                });
            }
        });
        (url, seen)
    }

    fn creds(url: String) -> Creds {
        Creds { token: "tok-123".into(), shell_secret: "s3cret".into(), url, pid: 0, started: String::new() }
    }

    #[test]
    fn identify_tells_our_server_from_others() {
        assert!(matches!(identify(&creds(fake(Fake::Right, "s3cret").0)), Identity::Ours));
        assert!(matches!(identify(&creds(fake(Fake::Wrong, "s3cret").0)), Identity::Stranger));
        assert!(matches!(identify(&creds(fake(Fake::NotFound, "s3cret").0)), Identity::Stranger));
        let free = TcpListener::bind("127.0.0.1:0").unwrap().local_addr().unwrap();
        assert!(matches!(identify(&creds(format!("http://{free}"))), Identity::Nobody));
    }

    #[test]
    fn request_sends_credentials_only_to_our_server() {
        let secret_headers = |seen: &Arc<Mutex<Vec<String>>>| {
            seen.lock().unwrap().iter().any(|h| h.contains("tok-123") || h.contains("s3cret") || h.to_ascii_lowercase().contains("x-truehand-shell"))
        };
        for mode in [Fake::Wrong, Fake::NotFound] {
            let (url, seen) = fake(mode, "s3cret");
            assert!(request(&creds(url), "POST", "/api/me", None, true, Duration::from_secs(3)).is_err());
            assert!(!seen.lock().unwrap().is_empty());
            assert!(!secret_headers(&seen));
        }
        let (url, seen) = fake(Fake::Right, "s3cret");
        let r = request(&creds(url), "POST", "/api/me", None, true, Duration::from_secs(3)).unwrap();
        assert_eq!(r.status, 200);
        assert!(secret_headers(&seen));
        let seen = seen.lock().unwrap();
        assert_eq!(seen.iter().filter(|h| *h == "CONNECT").count(), 1, "proof and request on one connection");
    }

    #[test]
    fn constant_eq_compares_whole_strings() {
        assert!(constant_eq(b"abc", b"abc"));
        assert!(!constant_eq(b"abc", b"abd"));
        assert!(!constant_eq(b"abc", b"ab"));
    }
}
