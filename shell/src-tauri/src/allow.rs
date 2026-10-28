pub const ROTATE: &str = "/api/token/rotate";

/// The only requests the page may send with the shell's secret: the ones that need a person
/// at the Mac, plus /api/me so the page knows it can ask for them. `*` is one path segment.
const HUMAN: &[(&str, &str)] = &[
    ("GET", "/api/me"),
    ("POST", "/api/projects/*/tasks/*/actions/approve-plan"),
    ("POST", "/api/projects/*/tasks/*/actions/push"),
    ("POST", "/api/projects/*/tasks/*/actions/open-pr"),
    ("POST", "/api/queued/*/confirm"),
    ("POST", "/api/pair/code"),
    ("DELETE", "/api/devices/*"),
    ("PUT", "/api/settings"),
    ("PUT", "/api/projects/*/config"),
    ("POST", "/api/projects/*/activate"),
    ("POST", "/api/projects/*/commit-config"),
    ("POST", "/api/machines"),
    ("POST", "/api/machines/*/update"),
    ("POST", ROTATE),
];

/// One path segment as the page may name it: no empty or dot segments, no query, fragment
/// or escapes.
pub fn segment_ok(s: &str) -> bool {
    !(s.is_empty() || s == "." || s == ".." || s.contains(['/', '?', '#', '%', '\\']))
}

pub enum MacKeyTarget<'a> {
    CommitConfig,
    Confirm(&'a str),
}

/// The requests whose OS dialog can tell this Mac's approver key from others, if the shell
/// adds `thisMacKey`: committing a config, or confirming a queued action that may be one.
pub fn mac_key_target<'a>(method: &str, path: &'a str) -> Option<MacKeyTarget<'a>> {
    if method != "POST" {
        return None;
    }
    let segs: Vec<&str> = path.split('/').collect();
    match segs.as_slice() {
        ["", "api", "projects", _, "commit-config"] => Some(MacKeyTarget::CommitConfig),
        ["", "api", "queued", qid, "confirm"] => Some(MacKeyTarget::Confirm(qid)),
        _ => None,
    }
}

pub fn human_request(method: &str, path: &str) -> bool {
    let segs: Vec<&str> = path.split('/').collect();
    if !path.starts_with('/') || !segs.iter().skip(1).all(|s| segment_ok(s)) {
        return false;
    }
    HUMAN.iter().any(|(m, pattern)| {
        let p: Vec<&str> = pattern.split('/').collect();
        *m == method && p.len() == segs.len() && p.iter().zip(&segs).all(|(a, b)| *a == "*" || a == b)
    })
}

#[cfg(test)]
mod tests {
    use super::human_request;

    #[test]
    fn allows_only_listed_requests() {
        assert!(human_request("POST", "/api/projects/p_1/tasks/T-1/actions/approve-plan"));
        assert!(human_request("POST", "/api/projects/m_1~p_1/tasks/T-1/actions/push"));
        assert!(human_request("POST", "/api/projects/p_1/tasks/T-1/actions/open-pr"));
        assert!(human_request("POST", "/api/queued/q_1/confirm"));
        assert!(human_request("PUT", "/api/projects/p_1/config"));
        assert!(human_request("GET", "/api/me"));
        assert!(human_request("PUT", "/api/settings"));
        assert!(human_request("POST", "/api/projects/p_1/activate"));
        assert!(human_request("POST", "/api/machines"));
        assert!(human_request("POST", "/api/machines/m_1/update"));
        assert!(!human_request("DELETE", "/api/machines/m_1"));
        assert!(!human_request("POST", "/api/machines/m_1/reconnect"));
        assert!(!human_request("POST", "/api/projects/p_1/tasks/T-1/actions/start-agent"));
        assert!(!human_request("GET", "/api/projects/p_1/tasks/T-1/actions/push"));
        assert!(!human_request("POST", "/api/projects/p_1/tasks/T-1/move"));
        assert!(!human_request("POST", "/api/projects/p_1/tasks/T-1/actions/push/x"));
        assert!(!human_request("POST", "/api/projects/../tasks/T-1/actions/push"));
        assert!(!human_request("POST", "/api/projects//tasks/T-1/actions/push"));
        assert!(!human_request("POST", "/api/projects/p_1/tasks/T-1/actions/push?x=1"));
        assert!(!human_request("POST", "/api/projects/p%2F1/tasks/T-1/actions/push"));
        assert!(human_request("DELETE", "/api/devices/d_1"));
        assert!(!human_request("DELETE", "/api/devices"));
        assert!(!human_request("POST", "/api/shutdown"));
        assert!(!human_request("POST", "api/me"));
        assert!(human_request("POST", "/api/projects/p_1/commit-config"));
    }

    #[test]
    fn finds_requests_that_take_this_mac_key() {
        use super::{mac_key_target, MacKeyTarget};
        assert!(matches!(mac_key_target("POST", "/api/projects/p_1/commit-config"), Some(MacKeyTarget::CommitConfig)));
        assert!(matches!(mac_key_target("POST", "/api/queued/q_1/confirm"), Some(MacKeyTarget::Confirm("q_1"))));
        assert!(mac_key_target("PUT", "/api/projects/p_1/commit-config").is_none());
        assert!(mac_key_target("POST", "/api/projects/p_1/tasks/T-1/actions/push").is_none());
    }
}
