const B = '\x1b[1m'
const D = '\x1b[2m'
const R = '\x1b[0m'
const GREEN = '\x1b[32m'
const RED = '\x1b[31m'
const CYAN = '\x1b[36m'
const YELLOW = '\x1b[33m'
const MAGENTA = '\x1b[35m'

const nl = '\r\n'

function banner(cwd: string): string {
  return (
    `${MAGENTA}╭──────────────────────────────────────────────╮${R}${nl}` +
    `${MAGENTA}│${R} ${B}✻ Claude Code${R}                                ${MAGENTA}│${R}${nl}` +
    `${MAGENTA}│${R}   ${D}cwd: ${cwd.padEnd(38)}${R}${MAGENTA}│${R}${nl}` +
    `${MAGENTA}╰──────────────────────────────────────────────╯${R}${nl}${nl}`
  )
}

const tool = (name: string, arg: string) => `${GREEN}⏺${R} ${B}${name}${R}(${arg})${nl}`
const say = (text: string) => `${R}⏺ ${text}${nl}${nl}`
const out = (text: string) => `  ${D}⎿${R}  ${text}${nl}`
const add = (n: number, text: string) => `      ${D}${String(n).padStart(3)}${R} ${GREEN}+ ${text}${R}${nl}`
const del = (n: number, text: string) => `      ${D}${String(n).padStart(3)}${R} ${RED}- ${text}${R}${nl}`
const prompt = (text: string) => `${D}>${R} ${text}${nl}${nl}`

export type Chunk = [delayMs: number, text: string]

export const transcripts: Record<string, Chunk[]> = {
  'cassa-14-dev': [
    [0, banner('~/Projects/app-cassa')],
    [120, prompt('Lavora sul task CASSA-14 seguendo il piano approvato.')],
    [180, say('Leggo il piano approvato e il contesto del task.')],
    [120, tool('Read', '.truehand/tasks/CASSA-14/plan.md')],
    [80, out('Lette 38 righe') + nl],
    [160, tool('Read', 'lib/auth/login_page.dart')],
    [80, out('Lette 112 righe') + nl],
    [200, say('Aggiungo il servizio biometrico con ripiego sul codice.')],
    [120, tool('Update', 'lib/auth/biometric_service.dart')],
    [
      100,
      out('Aggiornato lib/auth/biometric_service.dart: 18 aggiunte, 2 rimozioni') +
        del(12, 'Future<bool> authenticate() => Future.value(false);') +
        add(12, 'Future<bool> authenticate() async {') +
        add(13, "  if (!await _auth.canCheckBiometrics) return false;") +
        add(14, '  return _auth.authenticate(') +
        add(15, "    localizedReason: 'Accedi alla cassa',") +
        add(16, '    options: const AuthenticationOptions(biometricOnly: true),') +
        add(17, '  );') +
        add(18, '}') +
        nl,
    ],
    [200, tool('Bash', 'flutter test test/auth')],
    [400, out(`${GREEN}00:04 +14: Tutti i test sono passati!${R}`) + nl],
    [160, tool('Bash', 'truehand hand run analyze')],
    [300, out(`${GREEN}✓${R} analyze: nessun problema`) + nl],
    [160, tool('Bash', 'truehand hand test-proof')],
    [300, out(`${GREEN}✓${R} test-proof: i test passano con la modifica e falliscono senza`) + nl],
    [200, say('Ora confronto la schermata di accesso con il design.')],
    [120, tool('Bash', 'truehand hand eyes login')],
  ],
  'th-21-dev': [
    [0, banner('~/Projects/truehand')],
    [100, prompt('Riprendi TH-21 dal punto in cui eri rimasto.')],
    [160, tool('Read', 'web/src/views/Board.svelte')],
    [80, out('Lette 204 righe') + nl],
    [160, say('Il filtro per cliente funziona. Ho un dubbio prima di andare avanti.')],
    [
      140,
      `${YELLOW}?${R} ${B}Il filtro deve ricordarsi la scelta tra una sessione e l'altra,${R}${nl}` +
        `  ${B}o ripartire sempre da "Tutti"?${R}${nl}${nl}` +
        `  ${CYAN}❯ 1.${R} Ricorda l'ultimo filtro${nl}` +
        `    2. Riparti sempre da "Tutti"${nl}` +
        `    3. Rispondo nei commenti del task${nl}${nl}`,
    ],
  ],
  'cassa-12-plan': [
    [0, banner('~/Projects/app-cassa')],
    [100, prompt('Prepara il piano per CASSA-12.')],
    [160, tool('Read', 'lib/receipt/receipt_service.dart')],
    [80, out('Lette 87 righe') + nl],
    [160, tool('Write', '.truehand/tasks/CASSA-12/plan.md')],
    [80, out('Scritte 31 righe') + nl],
    [160, say('Il piano è pronto. Ho lasciato una domanda nei commenti: PDF allegato o link?')],
    [100, `${D}In attesa di approvazione del piano.${R}${nl}`],
  ],
  'th-18-dev': [
    [0, banner('~/Projects/truehand')],
    [100, prompt('Implementa la notifica quando un agente si ferma.')],
    [160, tool('Update', 'internal/notify/notify.go')],
    [80, out('Aggiornato internal/notify/notify.go: 41 aggiunte') + nl],
    [200, tool('Bash', 'truehand hand run e2e-notify')],
    [
      500,
      out(`${RED}✗ notifica non comparsa entro 30 s${R}`) +
        out(`${D}vedi prova "Notifica a schermo" nel task${R}`) +
        nl,
    ],
    [160, say('La prova a schermo non passa: la notifica parte ma il permesso di sistema non è concesso nel simulatore. Mi fermo qui.')],
  ],
  'th-23-review': [
    [0, banner('~/Projects/truehand')],
    [100, prompt('Fai la review di TH-23 contro il piano e le prove.')],
    [160, tool('Bash', 'git diff main...HEAD --stat')],
    [80, out('4 file modificati, 96 aggiunte, 12 rimozioni') + nl],
    [200, say('Il codice segue il piano, i test coprono i casi limite. Nessun problema bloccante.')],
    [100, `${GREEN}✓ Review completata${R}${nl}`],
  ],
  'free-permission': [
    [0, banner('~/Projects/truehand')],
    [100, prompt('Controlla perché il build della CI è lento.')],
    [160, tool('Bash', 'go test ./... -count=1 -json > /tmp/test.json')],
    [
      120,
      `${D}╭──────────────────────────────────────────────╮${R}${nl}` +
        `${D}│${R} ${B}Bash command${R}${nl}` +
        `${D}│${R}   go test ./... -count=1 -json > /tmp/test.json${nl}` +
        `${D}│${R}${nl}` +
        `${D}│${R} Do you want to proceed?${nl}` +
        `${D}│${R} ${CYAN}❯ 1. Yes${R}${nl}` +
        `${D}│${R}   2. Yes, and don't ask again for go test${nl}` +
        `${D}│${R}   3. No, and tell Claude what to do differently ${D}(esc)${R}${nl}` +
        `${D}╰──────────────────────────────────────────────╯${R}${nl}`,
    ],
  ],
  'cassa-9-dev': [
    [0, banner('~/Projects/app-cassa')],
    [100, prompt('Lavora su CASSA-9.')],
    [160, tool('Bash', 'flutter test')],
    [300, out(`${GREEN}00:09 +48: Tutti i test sono passati!${R}`) + nl],
    [100, `${GREEN}✓ Controlli superati, task verificato${R}${nl}`],
  ],
}

export const SPINNER = ['✻', '✶', '✳', '✢', '·', '✢', '✳', '✶']
