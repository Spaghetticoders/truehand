pub struct Text {
    pub open: &'static str,
    pub launch_at_login: &'static str,
    pub quit: &'static str,
    pub quit_title: &'static str,
    pub quit_body: &'static str,
    pub stop_server: &'static str,
    pub keep_server: &'static str,
    pub cancel: &'static str,
    pub cannot_start: &'static str,
    pub already_running: &'static str,
    pub unexpected: &'static str,
    pub exited: &'static str,
    pub timeout: &'static str,
    pub choose_folder: &'static str,
    pub sign_approve_plan: &'static str,
    pub sign_push: &'static str,
    pub sign_open_pr: &'static str,
    pub unrecognized: &'static str,
    pub signing_busy: &'static str,
}

const IT: Text = Text {
    open: "Apri Truehand",
    launch_at_login: "Apri al login",
    quit: "Esci",
    quit_title: "Uscire da Truehand?",
    quit_body: "Il server può restare attivo: gli agenti continuano a lavorare e il telefono resta collegato. Se lo fermi, gli agenti in corso vengono chiusi.",
    stop_server: "Ferma anche il server",
    keep_server: "Lascia attivo il server",
    cancel: "Annulla",
    cannot_start: "Impossibile avviare il server Truehand",
    already_running: "C'è già un server Truehand attivo che questa app non sa usare (avviato da terminale, o le credenziali nel Portachiavi sono state cancellate). Fermalo e riprova.",
    unexpected: "Il server ha risposto in modo inatteso",
    exited: "Il server si è chiuso durante l'avvio",
    timeout: "Il server non ha risposto entro 60 secondi",
    choose_folder: "Scegli la cartella del progetto",
    sign_approve_plan: "approvare il piano di {task} «{title}» in {repo} (piano {sha})",
    sign_push: "pushare il ramo {branch} di {task} «{title}» in {repo} (commit {sha})",
    sign_open_pr: "aprire la PR del ramo {branch} per {task} «{title}» in {repo} (commit {sha})",
    unrecognized: "Server non riconosciuto: all'indirizzo di Truehand risponde un processo che non conosce i segreti di questa app. Non gli è stato mandato nulla. Chiudi quel processo e riapri Truehand.",
    signing_busy: "C'è già una firma in corso",
};

const EN: Text = Text {
    open: "Open Truehand",
    launch_at_login: "Open at Login",
    quit: "Quit",
    quit_title: "Quit Truehand?",
    quit_body: "The server can keep running: agents keep working and your phone stays connected. If you stop it, running agents are closed.",
    stop_server: "Stop the server too",
    keep_server: "Keep the server running",
    cancel: "Cancel",
    cannot_start: "Could not start the Truehand server",
    already_running: "A Truehand server is already running that this app cannot use (started from a terminal, or its Keychain credentials were deleted). Stop it and try again.",
    unexpected: "The server answered unexpectedly",
    exited: "The server quit while starting",
    timeout: "The server did not answer within 60 seconds",
    choose_folder: "Choose the project folder",
    sign_approve_plan: "approve the plan of {task} “{title}” in {repo} (plan {sha})",
    sign_push: "push branch {branch} of {task} “{title}” in {repo} (commit {sha})",
    sign_open_pr: "open the PR of branch {branch} for {task} “{title}” in {repo} (commit {sha})",
    unrecognized: "Unrecognized server: whatever answers at Truehand's address does not know this app's secrets. Nothing was sent to it. Close that process and reopen Truehand.",
    signing_busy: "A signature is already in progress",
};

pub fn current() -> &'static Text {
    let out = std::process::Command::new("/usr/bin/defaults")
        .args(["read", "-g", "AppleLanguages"])
        .output();
    let first = out
        .ok()
        .map(|o| String::from_utf8_lossy(&o.stdout).to_string())
        .and_then(|s| s.split('"').nth(1).map(str::to_lowercase))
        .unwrap_or_default();
    if first.starts_with("it") {
        &IT
    } else {
        &EN
    }
}
