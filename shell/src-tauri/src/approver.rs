use std::fs;
use std::io::Write;
use std::os::raw::c_char;
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};
use std::path::Path;

use base64::Engine;
use serde::{Deserialize, Serialize};

/// What a person authorises; the server checks the same bytes (internal/sign Intent.Message).
#[derive(Clone, Serialize, Deserialize)]
pub struct Intent {
    pub action: String,
    pub repo: String,
    pub task: String,
    pub plan_sha256: String,
    #[serde(default)]
    pub head: String,
    pub nonce: String,
    pub time: String,
    pub approver: String,
}

#[derive(Serialize)]
pub struct Signed {
    pub intent: Intent,
    pub format: &'static str,
    pub sig: String,
}

fn is_hex(s: &str, lens: &[usize]) -> bool {
    lens.contains(&s.len()) && s.bytes().all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

fn id_ok(s: &str) -> bool {
    let b = s.as_bytes();
    !b.is_empty() && b.len() <= 64 && b[0].is_ascii_alphanumeric() && b.iter().all(|c| c.is_ascii_alphanumeric() || b"._-".contains(c))
}

/// The same rules as internal/sign Intent.Check: a signature only ever covers a well-formed
/// intent, whatever the server or the page sent.
pub fn validate(i: &Intent) -> Result<(), String> {
    let bad = |f: &str| Err(format!("intent: invalid {f}"));
    if !["approve-plan", "push", "open-pr"].contains(&i.action.as_str()) {
        return bad("action");
    }
    if i.repo.is_empty() || i.repo.len() > 200 || !i.repo.bytes().all(|b| b.is_ascii_graphic()) {
        return bad("repo");
    }
    if !id_ok(&i.task) {
        return bad("task");
    }
    if !is_hex(&i.plan_sha256, &[64]) {
        return bad("plan_sha256");
    }
    let needs_head = i.action != "approve-plan";
    if needs_head != !i.head.is_empty() || (needs_head && !is_hex(&i.head, &[40, 64])) {
        return bad("head");
    }
    if !(32..=64).contains(&i.nonce.len()) || !is_hex(&i.nonce, &[i.nonce.len()]) {
        return bad("nonce");
    }
    let t = i.time.as_bytes();
    let time_ok = t.len() == 20
        && t[19] == b'Z'
        && t.iter().enumerate().all(|(n, &c)| match n {
            4 | 7 => c == b'-',
            10 => c == b'T',
            13 | 16 => c == b':',
            19 => true,
            _ => c.is_ascii_digit(),
        });
    if !time_ok {
        return bad("time");
    }
    if !id_ok(&i.approver) {
        return bad("approver");
    }
    Ok(())
}

pub fn message(i: &Intent) -> Result<Vec<u8>, String> {
    validate(i)?;
    let fields = [
        ("action", &i.action),
        ("repo", &i.repo),
        ("task", &i.task),
        ("plan_sha256", &i.plan_sha256),
        ("head", &i.head),
        ("nonce", &i.nonce),
        ("time", &i.time),
        ("approver", &i.approver),
    ];
    let mut m = String::from("truehand-intent/1\n");
    for (k, v) in fields {
        m.push_str(k);
        m.push_str(": ");
        m.push_str(v);
        m.push('\n');
    }
    Ok(m.into_bytes())
}

const SPKI_P256_PREFIX: [u8; 26] = [
    0x30, 0x59, 0x30, 0x13, 0x06, 0x07, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x02, 0x01, 0x06, 0x08, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x03, 0x01, 0x07, 0x03, 0x42, 0x00,
];

fn spki_der(x963: &[u8]) -> Result<Vec<u8>, String> {
    if x963.len() != 65 || x963[0] != 4 {
        return Err("not an uncompressed P-256 point".into());
    }
    let mut der = SPKI_P256_PREFIX.to_vec();
    der.extend_from_slice(x963);
    Ok(der)
}

/// The first 12 hex of SHA-256 of the SPKI DER: how the server labels each approver's key, so
/// the page can tell this Mac's key from one somebody else put under the same name.
pub fn fingerprint(x963: &[u8]) -> Result<String, String> {
    Ok(crate::server::hex(&crate::server::sha256(&spki_der(x963)?))[..12].to_string())
}

/// PEM SubjectPublicKeyInfo from the 65-byte X9.63 point (04 || X || Y).
pub fn public_pem(x963: &[u8]) -> Result<String, String> {
    let der = spki_der(x963)?;
    let b64 = base64::engine::general_purpose::STANDARD.encode(der);
    let mut pem = String::from("-----BEGIN PUBLIC KEY-----\n");
    for chunk in b64.as_bytes().chunks(64) {
        pem.push_str(std::str::from_utf8(chunk).unwrap());
        pem.push('\n');
    }
    pem.push_str("-----END PUBLIC KEY-----\n");
    Ok(pem)
}

pub fn base64_std(der: &[u8]) -> String {
    base64::engine::general_purpose::STANDARD.encode(der)
}

extern "C" {
    fn th_se_create(biometry: bool, blob: *mut u8, blob_cap: usize, blob_len: *mut usize, pub_: *mut u8, pub_cap: usize, pub_len: *mut usize) -> i32;
    fn th_se_public(blob: *const u8, blob_len: usize, pub_: *mut u8, pub_cap: usize, pub_len: *mut usize) -> i32;
    fn th_se_sign(blob: *const u8, blob_len: usize, msg: *const u8, msg_len: usize, reason: *const c_char, sig: *mut u8, sig_cap: usize, sig_len: *mut usize) -> i32;
}

fn status(code: i32) -> Result<(), String> {
    match code {
        0 => Ok(()),
        1 => Err("this Mac has no Secure Enclave".into()),
        3 => Err("cancelled".into()),
        4 => Err("buffer too small".into()),
        _ => Err("Secure Enclave error".into()),
    }
}

pub struct SecureEnclaveKey {
    pub blob: Vec<u8>,
}

impl SecureEnclaveKey {
    pub fn create(biometry: bool) -> Result<Self, String> {
        let (mut blob, mut pubk) = (vec![0u8; 4096], vec![0u8; 128]);
        let (mut bl, mut pl) = (0usize, 0usize);
        status(unsafe { th_se_create(biometry, blob.as_mut_ptr(), blob.len(), &mut bl, pubk.as_mut_ptr(), pubk.len(), &mut pl) })?;
        blob.truncate(bl);
        Ok(Self { blob })
    }

    pub fn public_x963(&self) -> Result<Vec<u8>, String> {
        let mut pubk = vec![0u8; 128];
        let mut pl = 0usize;
        status(unsafe { th_se_public(self.blob.as_ptr(), self.blob.len(), pubk.as_mut_ptr(), pubk.len(), &mut pl) })?;
        pubk.truncate(pl);
        Ok(pubk)
    }

    pub fn sign(&self, msg: &[u8], reason: &str) -> Result<Vec<u8>, String> {
        let reason = std::ffi::CString::new(reason.replace('\0', "")).unwrap();
        let mut sig = vec![0u8; 128];
        let mut sl = 0usize;
        status(unsafe { th_se_sign(self.blob.as_ptr(), self.blob.len(), msg.as_ptr(), msg.len(), reason.as_ptr(), sig.as_mut_ptr(), sig.len(), &mut sl) })?;
        sig.truncate(sl);
        Ok(sig)
    }

    pub fn load(path: &Path) -> Option<Self> {
        fs::read(path).ok().map(|blob| Self { blob })
    }

    /// The blob is useless off this Mac's Secure Enclave, but it stays private all the same.
    pub fn load_or_create(path: &Path) -> Result<Self, String> {
        if let Ok(blob) = fs::read(path) {
            return Ok(Self { blob });
        }
        let key = Self::create(true)?;
        if let Some(dir) = path.parent() {
            fs::DirBuilder::new().recursive(true).mode(0o700).create(dir).map_err(|e| e.to_string())?;
        }
        let mut f = fs::OpenOptions::new().write(true).create_new(true).mode(0o600).open(path).map_err(|e| e.to_string())?;
        f.write_all(&key.blob).map_err(|e| e.to_string())?;
        Ok(key)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use security_framework::key::{Algorithm, GenerateKeyOptions, KeyType, SecKey, Token};

    fn intent() -> Intent {
        Intent {
            action: "approve-plan".into(),
            repo: "github.com/spaghetticoders/truehand".into(),
            task: "T-12".into(),
            plan_sha256: "a".repeat(64),
            head: String::new(),
            nonce: "0123456789abcdef0123456789abcdef".into(),
            time: "2026-10-27T22:10:00Z".into(),
            approver: "emanuel-mac".into(),
        }
    }

    #[test]
    fn message_matches_the_server_format() {
        let m = String::from_utf8(message(&intent()).unwrap()).unwrap();
        assert_eq!(
            m,
            format!(
                "truehand-intent/1\naction: approve-plan\nrepo: github.com/spaghetticoders/truehand\ntask: T-12\nplan_sha256: {}\nhead: \nnonce: 0123456789abcdef0123456789abcdef\ntime: 2026-10-27T22:10:00Z\napprover: emanuel-mac\n",
                "a".repeat(64)
            )
        );
    }

    #[test]
    fn message_refuses_malformed_intents() {
        let cases: Vec<fn(&mut Intent)> = vec![
            |i| i.task = "T-12\napprover: someone-else".into(),
            |i| i.action = "merge".into(),
            |i| i.repo = "github.com/x/y ".into(),
            |i| i.repo = String::new(),
            |i| i.plan_sha256 = "A".repeat(64),
            |i| i.plan_sha256 = "a".repeat(63),
            |i| i.head = "b".repeat(40),
            |i| {
                i.action = "push".into();
            },
            |i| {
                i.action = "push".into();
                i.head = "b".repeat(41);
            },
            |i| i.nonce = "0".repeat(31),
            |i| i.nonce = "g".repeat(32),
            |i| i.time = "2026-10-27T22:10:00.5Z".into(),
            |i| i.time = "2026-10-27 22:10:00Z".into(),
            |i| i.approver = "-emanuel".into(),
            |i| i.approver = "a b".into(),
            |i| i.task = ".T-1".into(),
        ];
        for (n, f) in cases.into_iter().enumerate() {
            let mut i = intent();
            f(&mut i);
            assert!(message(&i).is_err(), "case {n} accepted");
        }
        let mut push = intent();
        push.action = "push".into();
        push.head = "b".repeat(40);
        assert!(message(&push).is_ok());
    }

    /// A software key in memory only (never the Keychain, never Touch ID) signs the same way
    /// the Secure Enclave does: ECDSA P-256 over SHA-256, DER. TRUEHAND_SIGN_FIXTURE=<dir>
    /// writes message, signature and PEM there for a cross-check with the Go verifier.
    #[test]
    fn software_key_signs_in_the_server_format() {
        let mut o = GenerateKeyOptions::default();
        o.set_key_type(KeyType::ec_sec_prime_random()).set_size_in_bits(256).set_token(Token::Software);
        let key = SecKey::new(&o).unwrap();
        let x963 = key.public_key().unwrap().external_representation().unwrap().to_vec();
        let pem = public_pem(&x963).unwrap();
        assert!(pem.starts_with("-----BEGIN PUBLIC KEY-----\nMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE"));
        let msg = message(&intent()).unwrap();
        let der = key.create_signature(Algorithm::ECDSASignatureMessageX962SHA256, &msg).unwrap();
        assert_eq!(der[0], 0x30);
        assert!(key.public_key().unwrap().verify_signature(Algorithm::ECDSASignatureMessageX962SHA256, &msg, &der).unwrap());
        if let Ok(dir) = std::env::var("TRUEHAND_SIGN_FIXTURE") {
            let dir = Path::new(&dir);
            let signed = Signed { intent: intent(), format: "es256", sig: base64_std(&der) };
            fs::write(dir.join("signed.json"), serde_json::to_vec(&signed).unwrap()).unwrap();
            fs::write(dir.join("key.pem"), pem).unwrap();
            fs::write(dir.join("key.fingerprint"), fingerprint(&x963).unwrap()).unwrap();
        }
    }

    #[test]
    fn missing_key_is_not_created() {
        let dir = std::env::temp_dir().join(format!("th-approver-{}", std::process::id()));
        let path = dir.join("approver-se.key");
        assert!(SecureEnclaveKey::load(&path).is_none());
        assert!(!path.exists() && !dir.exists());
    }

    /// Creates a Secure Enclave key without biometry: no prompt, but it does use the Secure
    /// Enclave, so it is not run by default.
    #[test]
    #[ignore]
    fn secure_enclave_key_without_biometry() {
        let key = SecureEnclaveKey::create(false).unwrap();
        let pem = public_pem(&key.public_x963().unwrap()).unwrap();
        assert!(pem.contains("BEGIN PUBLIC KEY"));
        let sig = key.sign(&message(&intent()).unwrap(), "test").unwrap();
        assert_eq!(sig[0], 0x30);
    }
}
