use serde::{Deserialize, Serialize};

use crate::anchor::credential_ok;
use crate::server::{hex, sha256};

/// A passkey this Mac revoked for good, as internal/appstate Revocation: kept in the Keychain,
/// sent to the server at every start, matched by credential or by key.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Revocation {
    pub credential: String,
    #[serde(default)]
    pub public_key_sha256: String,
    /// The fingerprint people compare; only shown, revocations match on credential or key.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub key: String,
    #[serde(default)]
    pub label: String,
    pub time: String,
}

/// What revoking passkey `id` needs, from GET /api/passkeys: a listed passkey (then also
/// deleted from the server's file) or a soft revocation the server already keeps.
pub struct Target {
    pub revocation: Revocation,
    pub key: String,
    pub delete: bool,
}

fn sha256_hex_ok(s: &str) -> bool {
    s.len() == 64 && s.bytes().all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

pub fn target(id: &str, passkeys: &serde_json::Value, now: &str) -> Result<Target, String> {
    if !credential_ok(id) {
        return Err("revoke: invalid passkey id".into());
    }
    let find = |list: &str| passkeys[list].as_array().and_then(|a| a.iter().find(|p| p["id"] == id)).cloned();
    let text = |v: &serde_json::Value, k: &str| v[k].as_str().unwrap_or("").to_string();
    if let Some(p) = find("passkeys") {
        let pem = text(&p, "publicKey");
        let key = crate::anchor::pem_fingerprint(&pem)?;
        let revocation = Revocation { credential: id.into(), public_key_sha256: hex(&sha256(pem.trim().as_bytes())), key: key.clone(), label: text(&p, "label"), time: now.into() };
        return Ok(Target { revocation, key, delete: true });
    }
    if let Some(r) = find("revocations").filter(|r| r["revoked"] == "soft") {
        let sha = text(&r, "keySha256");
        if !sha.is_empty() && !sha256_hex_ok(&sha) {
            return Err("revoke: bad key hash".into());
        }
        let key = text(&r, "key");
        if !key.is_empty() && (key.len() != 12 || !key.bytes().all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))) {
            return Err("revoke: bad key fingerprint".into());
        }
        return Ok(Target { revocation: Revocation { credential: id.into(), public_key_sha256: sha, key: key.clone(), label: text(&r, "label"), time: now.into() }, key, delete: false });
    }
    Err("revoke: the server knows no passkey with that id".into())
}

/// The list with `r` added, once: an entry for the same credential and key is kept as it was.
pub fn add(mut list: Vec<Revocation>, r: Revocation) -> Vec<Revocation> {
    if !list.iter().any(|x| x.credential == r.credential && x.public_key_sha256 == r.public_key_sha256) {
        list.push(r);
    }
    list
}

/// What Touch ID signs to revoke: the signature is not kept, it only makes the revocation need
/// the person at this Mac.
pub fn message(r: &Revocation) -> Vec<u8> {
    format!("truehand-mac-revoke/1\ncredential: {}\npublic_key_sha256: {}\ntime: {}\n", r.credential, r.public_key_sha256, r.time).into_bytes()
}

fn civil_from_days(z: i64) -> (i64, i64, i64) {
    let z = z + 719468;
    let era = z.div_euclid(146097);
    let doe = z - era * 146097;
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    (yoe + era * 400 + if m <= 2 { 1 } else { 0 }, m, d)
}

/// RFC 3339 UTC with seconds, as Go's time.Time decodes it.
pub fn rfc3339(secs: i64) -> String {
    let (y, m, d) = civil_from_days(secs.div_euclid(86400));
    let s = secs.rem_euclid(86400);
    format!("{y:04}-{m:02}-{d:02}T{:02}:{:02}:{:02}Z", s / 3600, s % 3600 / 60, s % 60)
}

#[cfg(test)]
mod tests {
    use super::*;

    const PEM: &str = "-----BEGIN PUBLIC KEY-----\nMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE6Kb2b0Z7WkzjHgOAYH0KGkV1mKcE\nh3sDHBl8XyY6ZPsW8KZnN+Q3P7aeYpS1+0K3cXoC2J9b8RZzP6vjv6aB7w==\n-----END PUBLIC KEY-----\n";

    #[test]
    fn rfc3339_round_trips_with_the_intent_parser() {
        assert_eq!(rfc3339(0), "1970-01-01T00:00:00Z");
        assert_eq!(rfc3339(1_794_096_000), "2026-11-08T00:00:00Z");
        assert_eq!(rfc3339(1_709_251_199), "2024-02-29T23:59:59Z");
        for s in [0, 951_782_400, 1_709_251_199, 1_794_100_000, 4_102_444_800] {
            assert_eq!(crate::anchor::unix_time(&rfc3339(s)), Some(s));
        }
    }

    #[test]
    fn target_takes_a_listed_passkey_or_a_soft_revocation() {
        let list = serde_json::json!({
            "passkeys": [{ "id": "cred_1", "label": "S25", "publicKey": PEM, "key": "x" }],
            "revocations": [
                { "id": "cred_2", "key": "0123456789ab", "keySha256": "b".repeat(64), "label": "iPhone", "revoked": "soft" },
                { "id": "cred_5", "key": "0123", "keySha256": "b".repeat(64), "revoked": "soft" },
                { "id": "cred_3", "keySha256": "c".repeat(64), "revoked": "durable" },
                { "id": "cred_4", "keySha256": "B".repeat(64), "revoked": "soft" },
            ],
        });
        let t = target("cred_1", &list, "2026-11-08T01:00:00Z").unwrap();
        assert!(t.delete);
        assert_eq!(t.key, crate::anchor::pem_fingerprint(PEM).unwrap());
        assert_eq!(t.revocation, Revocation { credential: "cred_1".into(), public_key_sha256: hex(&sha256(PEM.trim().as_bytes())), key: t.key.clone(), label: "S25".into(), time: "2026-11-08T01:00:00Z".into() });
        let s = target("cred_2", &list, "2026-11-08T01:00:00Z").unwrap();
        assert!(!s.delete);
        assert_eq!((s.key.as_str(), s.revocation.key.as_str(), s.revocation.public_key_sha256.as_str(), s.revocation.label.as_str()), ("0123456789ab", "0123456789ab", "b".repeat(64).as_str(), "iPhone"));
        assert!(target("cred_5", &list, "t").is_err());
        assert!(target("cred_3", &list, "t").is_err());
        assert!(target("cred_4", &list, "t").is_err());
        assert!(target("cred_9", &list, "t").is_err());
        assert!(target("cred 1", &list, "t").is_err());
        let keyless = serde_json::json!({ "passkeys": [{ "id": "cred_1", "publicKey": "  " }] });
        assert!(target("cred_1", &keyless, "t").is_err());
    }

    #[test]
    fn add_keeps_one_entry_per_credential_and_key() {
        let r = |c: &str, k: &str, t: &str| Revocation { credential: c.into(), public_key_sha256: k.into(), key: String::new(), label: String::new(), time: t.into() };
        let l = add(vec![], r("a", "k1", "t1"));
        let l = add(l, r("a", "k1", "t2"));
        assert_eq!(l, vec![r("a", "k1", "t1")]);
        let l = add(l, r("a", "k2", "t3"));
        let l = add(l, r("b", "k1", "t4"));
        assert_eq!(l.len(), 3);
    }

    #[test]
    fn keychain_entry_is_what_the_server_decodes() {
        let r = Revocation { credential: "cred_1".into(), public_key_sha256: "a".repeat(64), key: "0123456789ab".into(), label: "S25 «x»".into(), time: rfc3339(1_794_100_000) };
        let v = serde_json::to_value(vec![r.clone()]).unwrap();
        assert_eq!(v[0]["public_key_sha256"], "a".repeat(64));
        assert_eq!(v[0]["time"], "2026-11-08T01:06:40Z");
        assert_eq!(v[0]["key"], "0123456789ab");
        let old: Vec<Revocation> = serde_json::from_str(r#"[{"credential":"c","public_key_sha256":"","label":"","time":"t"}]"#).unwrap();
        assert_eq!(old[0].key, "");
        assert_eq!(serde_json::from_value::<Vec<Revocation>>(v).unwrap(), vec![r.clone()]);
        assert!(String::from_utf8(message(&r)).unwrap().starts_with("truehand-mac-revoke/1\ncredential: cred_1\n"));
    }

    /// Go cross-check: TRUEHAND_REVOKE_SERVER=<dir> holds a real server's passkeys.json and
    /// the id to revoke; this writes the body the shell sends to POST /api/shell/revoked.
    #[test]
    fn builds_the_revoked_body_from_a_real_server() {
        let Ok(dir) = std::env::var("TRUEHAND_REVOKE_SERVER") else { return };
        let dir = std::path::Path::new(&dir);
        let passkeys: serde_json::Value = serde_json::from_slice(&std::fs::read(dir.join("passkeys.json")).unwrap()).unwrap();
        let id = std::fs::read_to_string(dir.join("id")).unwrap();
        let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().as_secs() as i64;
        let t = target(id.trim(), &passkeys, &rfc3339(now)).unwrap();
        std::fs::write(dir.join("delete"), t.delete.to_string()).unwrap();
        std::fs::write(dir.join("revoked.json"), serde_json::to_vec(&serde_json::json!({ "revoked": add(vec![], t.revocation) })).unwrap()).unwrap();
    }
}
