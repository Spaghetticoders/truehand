use std::collections::BTreeMap;

use base64::Engine;
use serde::{Deserialize, Serialize};

use crate::server::{hex, sha256};

pub const ACTION: &str = "anchor-owner";
/// The server keeps an anchor intent's nonce for two minutes; older ones would be refused.
const MAX_AGE_SECS: i64 = 120;

/// What the Mac app authorises to make a waiting passkey an owner; the server checks the same
/// bytes (internal/sign ServerIntent.Message).
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ServerIntent {
    pub action: String,
    pub server: String,
    pub subject: String,
    pub nonce: String,
    pub time: String,
    pub credential: String,
}

#[derive(Serialize)]
pub struct Signed {
    pub intent: ServerIntent,
    pub format: &'static str,
    pub sig: String,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Prepared {
    pub intent: ServerIntent,
    pub message: String,
    #[serde(default)]
    pub key: String,
    #[serde(default)]
    pub rp_id: String,
    #[serde(default)]
    pub public_key: String,
    /// The name of the device that registered the passkey, "" if unknown.
    #[serde(default)]
    pub device: String,
}

/// The fields of GET /api/passkeys the shell checks an anchor intent against.
#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Passkey {
    pub id: String,
    #[serde(default)]
    pub label: String,
    #[serde(default)]
    pub rp_id: String,
    #[serde(default)]
    pub owner: bool,
    #[serde(default)]
    pub owner_valid: bool,
    #[serde(default)]
    pub pending: String,
    #[serde(default)]
    pub public_key: String,
}

/// What the Touch ID prompt shows about the passkey being confirmed.
pub struct Checked {
    pub message: Vec<u8>,
    pub label: String,
    pub key: String,
}

/// An anchor intent checked against the server's own replies, ready to sign.
pub struct Ready {
    pub intent: ServerIntent,
    pub checked: Checked,
    pub device: Option<String>,
}

/// Checks the server's replies to GET /api/passkeys/{id}/anchor-intent, GET /api/passkeys
/// and GET /api/settings. With remote access through tailscale serve, its address must also
/// be the passkey's relying party.
pub fn check_replies(id: &str, prepared: serde_json::Value, passkeys: &serde_json::Value, settings: &serde_json::Value, now: i64) -> Result<Ready, String> {
    let p: Prepared = serde_json::from_value(prepared).map_err(|e| format!("anchor intent: {e}"))?;
    let pk: Passkey = passkeys["passkeys"]
        .as_array()
        .and_then(|a| a.iter().find(|p| p["id"] == id))
        .cloned()
        .ok_or("anchor intent: the server does not list that passkey")
        .and_then(|v| serde_json::from_value(v).map_err(|_| "anchor intent: bad passkey"))?;
    let remote = &settings["remote"];
    let host = match (remote["enabled"].as_bool(), remote["mode"].as_str()) {
        (Some(true), Some("tailscale-serve")) => Some(remote["address"].as_str().unwrap_or("").to_ascii_lowercase()),
        _ => None,
    };
    let checked = verify(id, &p, &pk, host.as_deref(), now)?;
    let device = Some(p.device.clone()).filter(|d| !d.is_empty());
    Ok(Ready { intent: p.intent, checked, device })
}

fn is_lower_hex(s: &str) -> bool {
    s.bytes().all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

/// internal/sign CheckRPID: a lowercase host name, not an IP address.
pub fn host_ok(s: &str) -> bool {
    let labels: Vec<&str> = s.split('.').collect();
    let label_ok = |l: &&str| {
        let b = l.as_bytes();
        (1..=63).contains(&b.len())
            && b.iter().all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || *c == b'-')
            && b[0] != b'-'
            && b[b.len() - 1] != b'-'
    };
    labels.iter().all(label_ok) && !labels.last().unwrap().bytes().all(|c| c.is_ascii_digit())
}

pub fn credential_ok(s: &str) -> bool {
    !s.is_empty() && s.len() <= 1364 && s.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-')
}

fn days_from_civil(y: i64, m: i64, d: i64) -> i64 {
    let y = if m <= 2 { y - 1 } else { y };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let doy = (153 * (m + if m > 2 { -3 } else { 9 }) + 2) / 5 + d - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146097 + doe - 719468
}

/// Seconds since the epoch of an intent time, only in the one form internal/sign accepts
/// (2006-01-02T15:04:05Z, a real date and time).
pub fn unix_time(s: &str) -> Option<i64> {
    let t = s.as_bytes();
    let shape = t.len() == 20
        && t.iter().enumerate().all(|(n, &c)| match n {
            4 | 7 => c == b'-',
            10 => c == b'T',
            13 | 16 => c == b':',
            19 => c == b'Z',
            _ => c.is_ascii_digit(),
        });
    if !shape {
        return None;
    }
    let num = |a: usize, b: usize| s[a..b].parse::<i64>().unwrap();
    let (y, mo, d, h, mi, se) = (num(0, 4), num(5, 7), num(8, 10), num(11, 13), num(14, 16), num(17, 19));
    let leap = (y % 4 == 0 && y % 100 != 0) || y % 400 == 0;
    let mdays = [31, if leap { 29 } else { 28 }, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
    if !(1..=12).contains(&mo) || d < 1 || d > mdays[(mo - 1) as usize] || h > 23 || mi > 59 || se > 59 {
        return None;
    }
    Some(days_from_civil(y, mo, d) * 86400 + h * 3600 + mi * 60 + se)
}

/// The same rules as internal/sign ServerIntent.Check.
pub fn check(i: &ServerIntent) -> Result<(), String> {
    let bad = |f: &str| Err(format!("anchor intent: invalid {f}"));
    let a = i.action.as_bytes();
    if a.is_empty() || a.len() > 64 || !a[0].is_ascii_lowercase() || !a.iter().all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || *c == b'-') {
        return bad("action");
    }
    if !host_ok(&i.server) {
        return bad("server");
    }
    if i.subject.len() != 64 || !is_lower_hex(&i.subject) {
        return bad("subject");
    }
    if !(32..=64).contains(&i.nonce.len()) || !is_lower_hex(&i.nonce) {
        return bad("nonce");
    }
    if !credential_ok(&i.credential) {
        return bad("credential");
    }
    if unix_time(&i.time).is_none() {
        return bad("time");
    }
    Ok(())
}

pub fn message(i: &ServerIntent) -> Result<Vec<u8>, String> {
    check(i)?;
    let mut m = String::from("truehand-server-intent/1\n");
    for (k, v) in [("action", &i.action), ("server", &i.server), ("subject", &i.subject), ("nonce", &i.nonce), ("time", &i.time), ("credential", &i.credential)] {
        m.push_str(k);
        m.push_str(": ");
        m.push_str(v);
        m.push('\n');
    }
    Ok(m.into_bytes())
}

/// internal/appstate Passkey.Binding through internal/sign Subject: sha256 of the passkey's
/// id, key and relying party as JSON with sorted keys. Only called on values that need no
/// JSON escaping, so the bytes are Go's.
pub fn subject(credential: &str, public_key_pem: &str, rp_id: &str) -> String {
    let mut m = BTreeMap::new();
    m.insert("credential", credential.to_string());
    m.insert("public_key_sha256", hex(&sha256(public_key_pem.trim().as_bytes())));
    m.insert("rp_id", rp_id.to_string());
    hex(&sha256(&serde_json::to_vec(&m).unwrap()))
}

/// A fingerprint as people compare it: "abcd ef01 2345".
pub fn grouped(key: &str) -> String {
    key.as_bytes().chunks(4).map(|c| String::from_utf8_lossy(c)).collect::<Vec<_>>().join(" ")
}

/// The first 12 hex of SHA-256 of the key's SPKI DER, as the server shows each key.
pub fn pem_fingerprint(pem: &str) -> Result<String, String> {
    let body = pem
        .trim()
        .strip_prefix("-----BEGIN PUBLIC KEY-----")
        .and_then(|s| s.strip_suffix("-----END PUBLIC KEY-----"))
        .ok_or("passkey: public key is not one PEM PUBLIC KEY block")?;
    let b64: String = body.chars().filter(|c| !c.is_ascii_whitespace()).collect();
    let der = base64::engine::general_purpose::STANDARD.decode(b64).map_err(|_| "passkey: public key is not base64")?;
    if der.first() != Some(&0x30) {
        return Err("passkey: public key is not DER".into());
    }
    Ok(hex(&sha256(&der))[..12].to_string())
}

/// Checks an intent the server prepared to anchor passkey `id` against what the page asked,
/// the passkey as the server lists it, the host it serves passkeys on and the clock: the
/// signature only ever covers this passkey becoming an owner of this server, now.
pub fn verify(id: &str, p: &Prepared, pk: &Passkey, served_host: Option<&str>, now: i64) -> Result<Checked, String> {
    let i = &p.intent;
    let msg = message(i)?;
    if i.action != ACTION {
        return Err("anchor intent: not an anchor-owner intent".into());
    }
    if i.credential != id || pk.id != id {
        return Err("anchor intent: prepared for another passkey".into());
    }
    if !pk.owner || pk.owner_valid || pk.pending != "mac" {
        return Err("anchor intent: that passkey is not waiting for this Mac".into());
    }
    if i.server != pk.rp_id || p.rp_id != pk.rp_id || p.public_key.trim() != pk.public_key.trim() {
        return Err("anchor intent: the server describes this passkey in two ways".into());
    }
    if let Some(host) = served_host.filter(|h| *h != pk.rp_id) {
        return Err(format!("anchor intent: the passkey is for {}, this server is reached at {host}", pk.rp_id));
    }
    if i.subject != subject(&pk.id, &pk.public_key, &pk.rp_id) {
        return Err("anchor intent: subject does not name this passkey's key".into());
    }
    let key = pem_fingerprint(&pk.public_key)?;
    if p.key != key {
        return Err("anchor intent: the server shows another key for this passkey".into());
    }
    let t = unix_time(&i.time).unwrap();
    if (now - t).abs() > MAX_AGE_SECS {
        return Err("anchor intent: not fresh, ask for a new one".into());
    }
    if base64::engine::general_purpose::STANDARD.encode(&msg) != p.message {
        return Err("anchor intent: the server's message differs from the intent".into());
    }
    Ok(Checked { message: msg, label: pk.label.clone(), key })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::approver::{base64_std, public_pem};
    use security_framework::key::{Algorithm, GenerateKeyOptions, KeyType, SecKey, Token};
    use std::fs;
    use std::path::Path;

    const PEM: &str = "-----BEGIN PUBLIC KEY-----\nMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE6Kb2b0Z7WkzjHgOAYH0KGkV1mKcE\nh3sDHBl8XyY6ZPsW8KZnN+Q3P7aeYpS1+0K3cXoC2J9b8RZzP6vjv6aB7w==\n-----END PUBLIC KEY-----\n";
    const NOW: i64 = 1_793_000_000;

    fn time_of(secs: i64) -> String {
        let days = secs.div_euclid(86400);
        let rem = secs.rem_euclid(86400);
        let mut y = 1970;
        let mut d = days;
        loop {
            let len = if (y % 4 == 0 && y % 100 != 0) || y % 400 == 0 { 366 } else { 365 };
            if d < len {
                break;
            }
            d -= len;
            y += 1;
        }
        let mut mo = 1;
        while d >= days_from_civil(y, mo + 1, 1) - days_from_civil(y, mo, 1) {
            d -= days_from_civil(y, mo + 1, 1) - days_from_civil(y, mo, 1);
            mo += 1;
        }
        format!("{y:04}-{mo:02}-{:02}T{:02}:{:02}:{:02}Z", d + 1, rem / 3600, rem % 3600 / 60, rem % 60)
    }

    fn passkey(pem: &str) -> Passkey {
        Passkey {
            id: "cred_AB-12".into(),
            label: "S25 Ultra".into(),
            rp_id: "mac.tail1234.ts.net".into(),
            owner: true,
            owner_valid: false,
            pending: "mac".into(),
            public_key: pem.into(),
        }
    }

    fn prepared(pk: &Passkey, now: i64) -> Prepared {
        let intent = ServerIntent {
            action: ACTION.into(),
            server: pk.rp_id.clone(),
            subject: subject(&pk.id, &pk.public_key, &pk.rp_id),
            nonce: "0123456789abcdef0123456789abcdef".into(),
            time: time_of(now),
            credential: pk.id.clone(),
        };
        let message = base64_std(&message(&intent).unwrap());
        Prepared { intent, message, key: pem_fingerprint(&pk.public_key).unwrap(), rp_id: pk.rp_id.clone(), public_key: pk.public_key.clone(), device: "Galaxy S25 Ultra".into() }
    }

    #[test]
    fn unix_time_matches_known_instants() {
        assert_eq!(unix_time("1970-01-01T00:00:00Z"), Some(0));
        assert_eq!(unix_time("2026-11-08T00:00:00Z"), Some(1_794_096_000));
        assert_eq!(unix_time("2024-02-29T23:59:59Z"), Some(1_709_251_199));
        for bad in ["2025-02-29T00:00:00Z", "2026-13-01T00:00:00Z", "2026-11-08T24:00:00Z", "2026-11-08T00:60:00Z", "2026-11-08T00:00:60Z", "2026-11-08T00:00:00.5Z", "2026-11-08 00:00:00Z", "2026-11-08T00:00:00+00:00", "2026-04-31T00:00:00Z", "2026-00-10T00:00:00Z"] {
            assert_eq!(unix_time(bad), None, "{bad}");
        }
        assert_eq!(time_of(NOW).len(), 20);
        assert_eq!(unix_time(&time_of(NOW)), Some(NOW));
    }

    #[test]
    fn grouped_splits_by_four() {
        assert_eq!(grouped("abcdef012345"), "abcd ef01 2345");
        assert_eq!(grouped(""), "");
    }

    #[test]
    fn message_matches_the_server_format() {
        let i = ServerIntent {
            action: ACTION.into(),
            server: "mac.tail1234.ts.net".into(),
            subject: "a".repeat(64),
            nonce: "0123456789abcdef0123456789abcdef".into(),
            time: "2026-11-08T01:00:00Z".into(),
            credential: "cred_AB-12".into(),
        };
        assert_eq!(
            String::from_utf8(message(&i).unwrap()).unwrap(),
            format!("truehand-server-intent/1\naction: anchor-owner\nserver: mac.tail1234.ts.net\nsubject: {}\nnonce: 0123456789abcdef0123456789abcdef\ntime: 2026-11-08T01:00:00Z\ncredential: cred_AB-12\n", "a".repeat(64))
        );
    }

    #[test]
    fn check_refuses_malformed_intents() {
        let pk = passkey(PEM);
        let cases: Vec<fn(&mut ServerIntent)> = vec![
            |i| i.credential = "cred\nserver: evil.ts.net".into(),
            |i| i.credential = "cred=".into(),
            |i| i.credential = String::new(),
            |i| i.credential = "a".repeat(1365),
            |i| i.action = "Anchor-owner".into(),
            |i| i.action = "1anchor".into(),
            |i| i.action = "a".repeat(65),
            |i| i.server = "Mac.ts.net".into(),
            |i| i.server = "mac.ts.net:443".into(),
            |i| i.server = "100.64.0.1".into(),
            |i| i.server = "-mac.ts.net".into(),
            |i| i.server = "mac..ts.net".into(),
            |i| i.server = format!("{}.ts.net", "a".repeat(64)),
            |i| i.subject = "A".repeat(64),
            |i| i.subject = "a".repeat(63),
            |i| i.nonce = "0".repeat(31),
            |i| i.nonce = "0".repeat(65),
            |i| i.nonce = "g".repeat(32),
            |i| i.time = "2026-11-08T01:00:00.1Z".into(),
        ];
        for (n, f) in cases.into_iter().enumerate() {
            let mut p = prepared(&pk, NOW);
            f(&mut p.intent);
            assert!(message(&p.intent).is_err(), "case {n} accepted");
        }
        assert!(host_ok(&format!("{}.ts.net", "a".repeat(63))));
    }

    #[test]
    fn verify_accepts_only_this_passkey_on_this_server_now() {
        let pk = passkey(PEM);
        let host = "mac.tail1234.ts.net";
        let ok = verify("cred_AB-12", &prepared(&pk, NOW), &pk, Some(host), NOW).unwrap();
        assert!(verify("cred_AB-12", &prepared(&pk, NOW), &pk, None, NOW).is_ok());
        assert_eq!(ok.label, "S25 Ultra");
        assert_eq!(ok.key.len(), 12);
        assert!(verify("cred_AB-12", &prepared(&pk, NOW - 120), &pk, Some(host), NOW).is_ok());

        let refused = |f: &dyn Fn(&mut Prepared, &mut Passkey, &mut String, &mut String, &mut i64)| {
            let (mut pk, mut id, mut host, mut now) = (passkey(PEM), "cred_AB-12".to_string(), host.to_string(), NOW);
            let mut p = prepared(&pk, NOW);
            f(&mut p, &mut pk, &mut id, &mut host, &mut now);
            verify(&id, &p, &pk, Some(&host), now).err().unwrap_or_else(|| panic!("accepted"))
        };
        let resign = |p: &mut Prepared| p.message = base64_std(&message(&p.intent).unwrap());
        assert!(refused(&|p, _, _, _, _| {
            p.intent.action = "grant-owner".into();
            resign(p)
        })
        .contains("anchor-owner"));
        assert!(refused(&|_, _, id, _, _| *id = "cred_other".into()).contains("another passkey"));
        assert!(refused(&|p, pk, id, _, _| {
            *id = "cred_other".into();
            pk.id = "cred_other".into();
            let _ = p;
        })
        .contains("another passkey"));
        assert!(refused(&|_, pk, _, _, _| pk.owner_valid = true).contains("not waiting"));
        assert!(refused(&|_, pk, _, _, _| pk.owner = false).contains("not waiting"));
        assert!(refused(&|_, pk, _, _, _| pk.pending = "owner".into()).contains("not waiting"));
        assert!(refused(&|_, _, _, host, _| *host = "other.tail1234.ts.net".into()).contains("reached at"));
        assert!(refused(&|_, _, _, host, _| host.clear()).contains("reached at"));
        assert!(refused(&|p, pk, _, _, _| {
            pk.rp_id = "evil.ts.net".into();
            p.rp_id = "evil.ts.net".into();
            p.intent.server = "evil.ts.net".into();
            p.intent.subject = subject(&pk.id, &pk.public_key, &pk.rp_id);
            resign(p)
        })
        .contains("reached at"));
        assert!(refused(&|p, _, _, _, _| {
            p.intent.server = "evil.ts.net".into();
            resign(p)
        })
        .contains("two ways"));
        assert!(refused(&|p, _, _, _, _| p.rp_id = "evil.ts.net".into()).contains("two ways"));
        assert!(refused(&|p, _, _, _, _| p.public_key = PEM.replace("6Kb2", "6Kb3")).contains("two ways"));
        assert!(refused(&|p, _, _, _, _| {
            p.intent.subject = "b".repeat(64);
            resign(p)
        })
        .contains("subject"));
        assert!(refused(&|p, pk, _, _, _| {
            pk.public_key = PEM.replace("6Kb2", "6Kb3");
            p.public_key = pk.public_key.clone();
        })
        .contains("subject"));
        assert!(refused(&|p, _, _, _, _| p.key = "000000000000".into()).contains("another key"));
        assert!(refused(&|_, _, _, _, now| *now += 121).contains("fresh"));
        assert!(refused(&|_, _, _, _, now| *now -= 121).contains("fresh"));
        assert!(refused(&|p, _, _, _, _| p.message = base64_std(b"truehand-server-intent/1\n")).contains("message differs"));
        assert!(refused(&|p, _, _, _, _| p.intent.nonce = "x".into()).contains("nonce"));
    }

    #[test]
    fn check_replies_takes_the_device_from_the_intent_and_the_host_from_tailscale_serve() {
        let pk = passkey(PEM);
        let prepared = |device: &str| {
            let p = prepared(&pk, NOW);
            serde_json::json!({ "intent": p.intent, "message": p.message, "label": "S25 Ultra", "key": p.key, "rpId": p.rp_id, "publicKey": p.public_key, "device": device, "synced": true })
        };
        let list = serde_json::json!({ "passkeys": [
            { "id": "cred_other", "owner": true, "pending": "mac", "rpId": "other.ts.net", "publicKey": PEM },
            { "id": pk.id, "label": pk.label, "rpId": pk.rp_id, "owner": true, "ownerValid": false, "pending": "mac", "device": "d_1", "publicKey": PEM },
        ] });
        let settings = |enabled: bool, mode: &str, addr: &str| serde_json::json!({ "remote": { "enabled": enabled, "mode": mode, "address": addr } });
        let serve = |addr: &str| settings(true, "tailscale-serve", addr);
        let ok = check_replies(&pk.id, prepared("Galaxy S25 Ultra"), &list, &serve("Mac.Tail1234.ts.net"), NOW).unwrap();
        assert_eq!(ok.device.as_deref(), Some("Galaxy S25 Ultra"));
        assert_eq!(ok.intent.credential, pk.id);
        assert_eq!(check_replies(&pk.id, prepared(""), &list, &serve(&pk.rp_id), NOW).unwrap().device, None);
        assert!(check_replies(&pk.id, prepared(""), &list, &serve("other.tail1234.ts.net"), NOW).err().unwrap().contains("reached at"));
        assert!(check_replies(&pk.id, prepared(""), &list, &serve(""), NOW).err().unwrap().contains("reached at"));
        assert!(check_replies(&pk.id, prepared(""), &list, &settings(false, "tailscale-serve", "other.ts.net"), NOW).is_ok());
        assert!(check_replies(&pk.id, prepared(""), &list, &settings(true, "tailnet", "100.64.0.1"), NOW).is_ok());
        assert!(check_replies(&pk.id, prepared(""), &list, &serde_json::json!({}), NOW).is_ok());
        assert!(check_replies(&pk.id, prepared(""), &serde_json::json!({ "passkeys": [] }), &serve(&pk.rp_id), NOW).err().unwrap().contains("does not list"));
    }

    #[test]
    fn fingerprint_is_the_one_the_server_shows_for_the_mac_key() {
        let mut o = GenerateKeyOptions::default();
        o.set_key_type(KeyType::ec_sec_prime_random()).set_size_in_bits(256).set_token(Token::Software);
        let key = SecKey::new(&o).unwrap();
        let x963 = key.public_key().unwrap().external_representation().unwrap().to_vec();
        assert_eq!(pem_fingerprint(&public_pem(&x963).unwrap()).unwrap(), crate::approver::fingerprint(&x963).unwrap());
        assert!(pem_fingerprint("-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----").is_err());
    }

    /// A software key in memory only signs an anchor intent the way the Secure Enclave does.
    #[test]
    fn software_key_signs_an_anchor_intent_in_the_server_format() {
        let mut o = GenerateKeyOptions::default();
        o.set_key_type(KeyType::ec_sec_prime_random()).set_size_in_bits(256).set_token(Token::Software);
        let mac = SecKey::new(&o).unwrap();
        let phone = SecKey::new(&o).unwrap();
        let phone_pem = public_pem(&phone.public_key().unwrap().external_representation().unwrap()).unwrap();
        let pk = passkey(&phone_pem);
        let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().as_secs() as i64;
        let p = prepared(&pk, now);
        let checked = verify(&pk.id, &p, &pk, Some(&pk.rp_id), now).unwrap();
        let der = mac.create_signature(Algorithm::ECDSASignatureMessageX962SHA256, &checked.message).unwrap();
        assert!(mac.public_key().unwrap().verify_signature(Algorithm::ECDSASignatureMessageX962SHA256, &checked.message, &der).unwrap());
    }

    /// The other half of the Go cross-check: TRUEHAND_ANCHOR_SERVER=<dir> holds the replies a
    /// real internal/serve gave (anchor-intent.json, passkeys.json, settings.json, now, id);
    /// this checks them as the command does and writes the signature and the Mac key back.
    #[test]
    fn checks_and_signs_replies_from_a_real_server() {
        let Ok(dir) = std::env::var("TRUEHAND_ANCHOR_SERVER") else { return };
        let dir = Path::new(&dir);
        let read = |f: &str| serde_json::from_slice::<serde_json::Value>(&fs::read(dir.join(f)).unwrap()).unwrap();
        let id = fs::read_to_string(dir.join("id")).unwrap();
        let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().as_secs() as i64;
        let ready = check_replies(id.trim(), read("anchor-intent.json"), &read("passkeys.json"), &read("settings.json"), now).unwrap();
        assert_eq!(ready.device.as_deref(), std::fs::read_to_string(dir.join("device")).ok().as_deref());
        let mut o = GenerateKeyOptions::default();
        o.set_key_type(KeyType::ec_sec_prime_random()).set_size_in_bits(256).set_token(Token::Software);
        let mac = SecKey::new(&o).unwrap();
        let der = mac.create_signature(Algorithm::ECDSASignatureMessageX962SHA256, &ready.checked.message).unwrap();
        let signed = Signed { intent: ready.intent, format: "es256", sig: base64_std(&der) };
        fs::write(dir.join("anchor.json"), serde_json::to_vec(&serde_json::json!({ "signed": signed })).unwrap()).unwrap();
        fs::write(dir.join("mac.pem"), public_pem(&mac.public_key().unwrap().external_representation().unwrap()).unwrap()).unwrap();
        fs::write(dir.join("label"), ready.checked.label).unwrap();
        fs::write(dir.join("key"), ready.checked.key).unwrap();
    }
}
