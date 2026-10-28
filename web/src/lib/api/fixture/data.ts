import type { Actor, Activation, Check, Client, Comment, Device, DiffFile, Machine, Project, Proof, QueuedAction, Session, Settings, TaskDetail, TaskState } from '../types'

export const ago = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString()

export const me: Actor = { id: 'human:dev@example.com', kind: 'human', name: 'Dev' }
const dev = (taskId: string): Actor => ({ id: `claude:${taskId}-dev`, kind: 'agent-dev', name: 'Claude', taskId })
const rev = (taskId: string): Actor => ({ id: `claude:${taskId}-review`, kind: 'agent-review', name: 'Claude', taskId })
const unbound: Actor = { id: 'claude:pid-48211', kind: 'agent', name: 'pid-48211' }

export const machines: Machine[] = [
  { id: 'local', label: 'MacBook Pro', os: 'darwin', arch: 'arm64', status: 'online', version: '0.9.0', tools: { claude: true, git: true, gh: true }, canApprove: true },
  { id: 'm_a1b2c3d4', label: 'Mac mini', sshTarget: 'mac-mini.esempio.ts.net', os: 'darwin', arch: 'arm64', status: 'online', version: '0.9.0', tools: { claude: true, git: true, gh: false }, canApprove: false },
  { id: 'm_9f8e7d6c', label: 'PC gaming', sshTarget: 'pcgaming', os: 'linux', arch: 'amd64', status: 'offline', version: '0.9.0', tools: { claude: true, git: true, gh: true }, canApprove: false },
]

export const clients: Client[] = [
  { id: 'acme-retail', name: 'Acme Retail' },
  { id: 'spaghetticoders', name: 'Spaghetticoders' },
]

export const projects: Project[] = [
  { id: 'p_1a2b3c4d', clientId: 'acme-retail', name: 'App Cassa', path: '~/Projects/app-cassa', key: 'CASSA', active: true, branch: 'main', machineId: 'local' },
  { id: 'p_5e6f7a8b', clientId: 'acme-retail', name: 'Sito vetrina', path: '~/Projects/sito-vetrina', key: '', active: false, branch: 'main', machineId: 'local' },
  { id: 'm_a1b2c3d4~p_77aa8899', clientId: 'acme-retail', name: 'Gestionale ordini', path: '~/Projects/gestionale-ordini', key: 'GO', active: true, branch: 'main', machineId: 'm_a1b2c3d4' },
  { id: 'p_9c0d1e2f', clientId: 'spaghetticoders', name: 'Truehand', path: '~/Projects/truehand', key: 'TH', active: true, branch: 'feat/app-v2', machineId: 'local' },
  { id: 'm_9f8e7d6c~p_31bc22aa', clientId: 'spaghetticoders', name: 'Wovio', path: '~/code/wovio', key: 'WV', active: true, branch: 'main', machineId: 'm_9f8e7d6c' },
]

export type Row = Omit<TaskDetail, 'waitReason' | 'primaryAction' | 'sessionIds' | 'readOnly' | 'planReady' | 'hasOpenQuestion'> & {
  /** Facts the server would read from the gate. */
  planReadyFact: boolean
  deliverGateOpen: boolean
  patches: Record<string, string>
}

const PLAN_CASSA_12 = `## Obiettivo
Dopo il pagamento il cliente può ricevere lo scontrino via email.

## Passi
1. Campo email facoltativo nella schermata di pagamento
2. Servizio \`ReceiptMailer\` che genera il PDF dallo scontrino esistente
3. Invio in coda, con nuovo tentativo se la rete manca
4. Test sul servizio e sulla coda

## Fuori dal perimetro
- Modelli email personalizzati per negozio

## Come lo verifichiamo
- [ ] Test automatici del servizio
- [ ] Screenshot della schermata di pagamento confrontato con il design
- [ ] Email di prova ricevuta su casella di test`

const PLAN_GENERIC = `## Obiettivo
Implementare quanto descritto nel task senza toccare altre aree.

## Passi
1. Leggere il codice esistente e i test
2. Modificare solo i file nel perimetro
3. Aggiungere i test mancanti
4. Raccogliere le prove (test, screenshot)`

const PATCH_LOGIN = `@@ -9,7 +9,16 @@ class BiometricService {
   final LocalAuthentication _auth;

-  Future<bool> authenticate() => Future.value(false);
+  Future<bool> authenticate() async {
+    if (!await _auth.canCheckBiometrics) return false;
+    return _auth.authenticate(
+      localizedReason: 'Accedi alla cassa',
+      options: const AuthenticationOptions(biometricOnly: true),
+    );
+  }
 }`

const PATCH_PUBSPEC = `@@ -31,6 +31,7 @@ dependencies:
   flutter_riverpod: ^2.6.1
+  local_auth: ^2.3.0
   go_router: ^14.6.2`

const PATCH_CSV = `@@ -1,4 +1,12 @@
+import 'package:csv/csv.dart';
+
 class SalesReport {
   final List<Sale> sales;
+
+  String toCsv() => const ListToCsvConverter().convert([
+        ['data', 'scontrini', 'totale'],
+        for (final d in byDay) [d.date, d.count, d.total],
+      ]);
 }`

let cid = 0
const comment = (minutes: number, actor: Actor, body: string): Comment => ({ id: `c${cid++}`, time: ago(minutes), actor, body })
const check = (id: string, gate: TaskState, status: Check['status'], detail?: string): Check => ({ id, gate, status, detail })
const proof = (id: string, title: string, verdict: 'pass' | 'fail', summary: string, actor: Actor, minutes: number, extra: Partial<Proof> = {}): Proof => ({
  id,
  title,
  verdict,
  summary,
  actor,
  time: ago(minutes),
  fresh: true,
  intact: true,
  images: [],
  ...extra,
})
const file = (path: string, added: number, removed: number, inScope = true, status: DiffFile['status'] = 'M'): DiffFile => ({ path, added, removed, status, inScope })
const later = (from: TaskState): Check[] => {
  const all: [TaskState, string][] = [
    ['verified', 'test_proof'],
    ['verified', 'hand_passed'],
    ['reviewed', 'review_by_other_role'],
    ['delivered', 'evidence_matches_head'],
    ['delivered', 'conventional_commits'],
  ]
  const order: TaskState[] = ['planned', 'approved', 'building', 'verified', 'reviewed', 'delivered']
  return all.filter(([g]) => order.indexOf(g) > order.indexOf(from) + 1).map(([g, id]) => check(id, g, 'pending'))
}

function row(r: Partial<Row> & Pick<Row, 'id' | 'projectId' | 'title' | 'state'> & { updated: number }): Row {
  const { updated, ...rest } = r
  return {
    merged: false,
    pushed: false,
    failing: false,
    stale: false,
    updatedAt: ago(updated),
    description: '',
    scope: [],
    comments: [],
    planProblems: [],
    checks: [],
    proofs: [],
    diff: [],
    history: [],
    approvals: [],
    planReadyFact: false,
    deliverGateOpen: false,
    patches: {},
    ...rest,
  }
}

export const rows: Row[] = [
  row({
    id: 'CASSA-12',
    projectId: 'p_1a2b3c4d',
    title: 'Scontrino digitale via email',
    state: 'planned',
    updated: 14,
    worktree: '~/.truehand/worktrees/p_1a2b3c4d/CASSA-12',
    externalTicket: 'LIN-482',
    scope: ['lib/receipt/**', 'test/receipt/**'],
    description:
      'Alla fine del pagamento il cassiere può inserire l’email del cliente e mandargli lo scontrino.\n\nRichiesta arrivata dal negozio di via Sparano: **molti clienti non vogliono la carta**.\n\n<script>alert(2)</script><img src=x onerror="alert(1)" hidden>',
    comments: [
      comment(90, me, 'Priorità alta, il cliente lo chiede da settimane.'),
      { ...comment(16, dev('CASSA-12'), 'Piano pronto. Domanda: lo scontrino va **allegato in PDF** o basta un **link** alla versione online?'), openQuestion: true },
    ],
    plan: PLAN_CASSA_12,
    planSha256: '9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08',
    planReadyFact: true,
    checks: [check('plan_valid', 'approved', 'pass'), check('human_approval', 'approved', 'fail', 'no approval of plan.md sha 9f86d0'), ...later('planned')],
  }),
  row({
    id: 'CASSA-14',
    projectId: 'p_1a2b3c4d',
    title: 'Accesso con Face ID',
    state: 'building',
    updated: 1,
    worktree: '~/.truehand/worktrees/p_1a2b3c4d/CASSA-14',
    scope: ['lib/auth/**', 'test/auth/**'],
    description: 'Il cassiere entra con Face ID; se non è disponibile usa il codice a 6 cifre.',
    comments: [comment(240, me, 'Piano approvato.'), comment(30, dev('CASSA-14'), 'Iniziato lo sviluppo. Test automatici verdi, preparo lo screenshot.')],
    plan: PLAN_GENERIC,
    planSha256: 'aa11',
    checks: [check('test_proof', 'verified', 'pass'), check('hand_passed', 'verified', 'pass', 'analyze passed'), check('eyes', 'verified', 'fail', 'no evidence for screen login'), ...later('building')],
    proofs: [proof('e1', 'test-proof', 'pass', '14 test superati in 4 s', dev('CASSA-14'), 3), proof('e2', 'analyze', 'pass', 'Nessun problema', dev('CASSA-14'), 3)],
    diff: [file('lib/auth/biometric_service.dart', 9, 1), file('pubspec.yaml', 1, 0, false)],
    patches: { 'lib/auth/biometric_service.dart': PATCH_LOGIN, 'pubspec.yaml': PATCH_PUBSPEC },
    approvals: [{ time: ago(240), actor: me, via: 'app+os-auth:macos', current: true }],
    history: [
      { time: ago(600), type: 'created', actor: me },
      { time: ago(240), type: 'move', actor: me, from: 'planned', to: 'approved' },
      { time: ago(34), type: 'move', actor: dev('CASSA-14'), from: 'approved', to: 'building' },
    ],
  }),
  row({
    id: 'CASSA-9',
    projectId: 'p_1a2b3c4d',
    title: 'Esporta report vendite in CSV',
    state: 'verified',
    updated: 120,
    worktree: '~/.truehand/worktrees/p_1a2b3c4d/CASSA-9',
    scope: ['lib/report/**'],
    description: 'Dalla pagina report, un pulsante scarica le vendite del mese in CSV (data, scontrini, totale).',
    comments: [comment(125, dev('CASSA-9'), 'Fatto. Tutti i controlli sono verdi, pronto per la review.')],
    plan: PLAN_GENERIC,
    checks: [check('review_by_other_role', 'reviewed', 'fail', 'no review yet'), ...later('verified')],
    proofs: [
      proof('e1', 'test-proof', 'pass', '48 test superati', dev('CASSA-9'), 126),
      proof('e2', 'eyes', 'pass', 'Uguale al design (0,4% di pixel diversi)', dev('CASSA-9'), 126, { images: [{ url: 'evidence/report.svg', caption: 'report.png' }] }),
    ],
    diff: [file('lib/report/sales_report.dart', 8, 0)],
    patches: { 'lib/report/sales_report.dart': PATCH_CSV },
  }),
  row({
    id: 'CASSA-11',
    projectId: 'p_1a2b3c4d',
    title: 'Sconti a percentuale sui reparti',
    state: 'reviewed',
    updated: 50,
    worktree: '~/.truehand/worktrees/p_1a2b3c4d/CASSA-11',
    description: 'Sconto configurabile per reparto.',
    comments: [comment(55, rev('CASSA-11'), 'Review ok.')],
    plan: PLAN_GENERIC,
    deliverGateOpen: true,
    checks: [check('evidence_matches_head', 'delivered', 'pass'), check('conventional_commits', 'delivered', 'pass')],
    proofs: [proof('e1', 'test-proof', 'pass', '22 test superati', dev('CASSA-11'), 70), proof('e2', 'review', 'pass', 'Nessun problema bloccante', rev('CASSA-11'), 55)],
  }),
  row({
    id: 'CASSA-7',
    projectId: 'p_1a2b3c4d',
    title: 'Correggi arrotondamento IVA sugli sconti',
    state: 'delivered',
    updated: 300,
    worktree: '~/.truehand/worktrees/p_1a2b3c4d/CASSA-7',
    scope: ['lib/pricing/**'],
    description: 'Con sconto percentuale l’IVA veniva arrotondata riga per riga invece che sul totale.',
    comments: [comment(320, rev('CASSA-7'), 'Review ok: il calcolo ora segue la normativa, aggiunti 6 casi di test.')],
    plan: PLAN_GENERIC,
    proofs: [proof('e1', 'test-proof', 'pass', '52 test superati', dev('CASSA-7'), 330)],
  }),
  row({
    id: 'CASSA-3',
    projectId: 'p_1a2b3c4d',
    title: 'Onboarding nuovo negozio',
    state: 'delivered',
    updated: 60 * 24 * 6,
    merged: true,
    pushed: true,
    prUrl: 'https://github.com/acme/app-cassa/pull/41',
    scope: ['lib/onboarding/**'],
    description: 'Procedura guidata in tre passi per configurare un nuovo negozio.',
    comments: [comment(60 * 24 * 6, me, 'Integrato in main.')],
    plan: PLAN_GENERIC,
    proofs: [proof('e1', 'eyes', 'pass', 'Uguale al design', unbound, 60 * 24 * 7, { fresh: false, images: [{ url: 'evidence/login.svg', caption: 'login.png' }] })],
  }),
  row({ id: 'CASSA-2', projectId: 'p_1a2b3c4d', title: 'Login con codice negozio', state: 'delivered', updated: 60 * 24 * 12, merged: true, pushed: true }),
  row({
    id: 'TH-21',
    projectId: 'p_9c0d1e2f',
    title: 'Filtro per cliente nella board',
    state: 'building',
    updated: 6,
    worktree: '~/.truehand/worktrees/p_9c0d1e2f/TH-21',
    scope: ['web/src/**'],
    description: 'Nella board, un filtro per vedere solo i progetti di un cliente.',
    plan: PLAN_GENERIC,
    checks: [check('test_proof', 'verified', 'fail', 'no evidence from test-proof'), ...later('building')],
  }),
  row({
    id: 'TH-24',
    projectId: 'p_9c0d1e2f',
    title: 'Esporta la board in CSV',
    state: 'building',
    updated: 60 * 5,
    branch: 'feat/export',
    description: 'Letto da un altro ramo.',
  }),
  row({
    id: 'TH-18',
    projectId: 'p_9c0d1e2f',
    title: 'Notifica quando un agente si ferma',
    state: 'building',
    updated: 42,
    failing: true,
    worktree: '~/.truehand/worktrees/p_9c0d1e2f/TH-18',
    scope: ['internal/notify/**'],
    description: 'Quando un agente aspetta una risposta compare una notifica di sistema.',
    comments: [comment(41, dev('TH-18'), 'La prova a schermo non passa: nel simulatore manca il permesso per le notifiche.')],
    plan: PLAN_GENERIC,
    checks: [check('test_proof', 'verified', 'pass'), check('hand_passed', 'verified', 'fail', 'e2e-notify failed'), ...later('building')],
    proofs: [
      proof('e1', 'test-proof', 'pass', '9 test superati', dev('TH-18'), 44),
      proof('e2', 'e2e-notify', 'fail', 'Nessuna notifica comparsa entro 30 secondi', dev('TH-18'), 42, {
        images: [{ url: 'evidence/toast.svg', caption: 'toast.png' }],
        log: 'atteso: notifica visibile\ntrovato: nessuna notifica dopo 30 s\npermesso notifiche: negato',
      }),
    ],
  }),
  row({
    id: 'TH-23',
    projectId: 'p_9c0d1e2f',
    title: 'Nomi leggibili al posto degli identificativi',
    state: 'planned',
    updated: 25,
    worktree: '~/.truehand/worktrees/p_9c0d1e2f/TH-23',
    scope: ['internal/core/**'],
    description: 'Commenti e prove mostrano «Claude · sviluppo» o il nome della persona, mai un identificativo tecnico.',
    plan: PLAN_GENERIC,
    planSha256: 'bb22',
    planReadyFact: true,
    checks: [check('plan_valid', 'approved', 'pass'), check('human_approval', 'approved', 'fail', 'no approval of plan.md sha bb22'), ...later('planned')],
  }),
  row({
    id: 'TH-22',
    projectId: 'p_9c0d1e2f',
    title: 'Impostazioni in italiano e inglese',
    state: 'approved',
    updated: 200,
    scope: ['web/src/lib/i18n/**'],
    description: 'Tutta l’interfaccia tradotta, lingua scelta dalle impostazioni.',
    plan: PLAN_GENERIC,
    checks: later('approved'),
  }),
  row({
    id: 'TH-15',
    projectId: 'p_9c0d1e2f',
    title: 'Ricerca nei task',
    state: 'delivered',
    updated: 60 * 30,
    pushed: true,
    prUrl: 'https://github.com/spaghetticoders/truehand/pull/88',
    description: 'Campo di ricerca sopra la board.',
  }),
  row({ id: 'SITO-2', projectId: 'p_5e6f7a8b', title: 'Pagina contatti con mappa', state: 'planned', updated: 60 * 26, description: 'Modulo contatti e mappa del negozio.' }),
  row({
    id: 'GO-4',
    projectId: 'm_a1b2c3d4~p_77aa8899',
    title: 'Stampa bolla di consegna',
    state: 'planned',
    updated: 33,
    worktree: '~/.truehand/worktrees/p_77aa8899/GO-4',
    description: 'PDF della bolla dalla scheda ordine.',
    plan: PLAN_GENERIC,
    planSha256: 'cc33',
    planReadyFact: true,
    checks: [check('plan_valid', 'approved', 'pass'), check('human_approval', 'approved', 'fail', 'no approval')],
  }),
  row({ id: 'GO-2', projectId: 'm_a1b2c3d4~p_77aa8899', title: 'Filtro ordini per stato', state: 'building', updated: 90, worktree: '~/.truehand/worktrees/p_77aa8899/GO-2', description: 'Filtro per stato nella lista ordini.', plan: PLAN_GENERIC }),
  row({ id: 'WV-9', projectId: 'm_9f8e7d6c~p_31bc22aa', title: 'Timer di recupero tra le serie', state: 'building', updated: 60 * 20, description: 'Timer configurabile tra una serie e l’altra.' }),
]

const sess = (id: string, projectId: string, taskId: string | undefined, role: Session['role'], status: Session['status'], startedMin: number, extra: Partial<Session> = {}): Session => ({
  id,
  projectId,
  taskId,
  role,
  status,
  startedAt: ago(startedMin),
  resumable: false,
  permissionMode: 'default',
  tail: [],
  machineId: projectId.includes('~') ? projectId.split('~')[0] : 'local',
  ...extra,
})

export const sessions: Session[] = [
  sess('s_dev14', 'p_1a2b3c4d', 'CASSA-14', 'dev', 'working', 34),
  sess('s_dev21', 'p_9c0d1e2f', 'TH-21', 'dev', 'waiting', 48, { waitingFor: 'input' }),
  sess('s_dev12', 'p_1a2b3c4d', 'CASSA-12', 'dev', 'idle', 22),
  sess('s_dev18', 'p_9c0d1e2f', 'TH-18', 'dev', 'exited', 70, { endedAt: ago(42), exitCode: 1, resumable: true }),
  sess('s_dev11', 'p_1a2b3c4d', 'CASSA-11', 'dev', 'exited', 120, { endedAt: ago(80), exitCode: 0, resumable: true }),
  sess('s_rev11', 'p_1a2b3c4d', 'CASSA-11', 'review', 'exited', 60, { endedAt: ago(55), exitCode: 0 }),
  sess('s_dev9', 'p_1a2b3c4d', 'CASSA-9', 'dev', 'exited', 150, { endedAt: ago(124), exitCode: 0, resumable: true }),
  sess('s_free1', 'p_9c0d1e2f', undefined, 'free', 'waiting', 12, { waitingFor: 'permission' }),
  sess('m_a1b2c3d4~s_dev2', 'm_a1b2c3d4~p_77aa8899', 'GO-2', 'dev', 'working', 90),
]

export const transcriptOf: Record<string, string> = {
  s_dev14: 'cassa-14-dev',
  s_dev21: 'th-21-dev',
  s_dev12: 'cassa-12-plan',
  s_dev18: 'th-18-dev',
  s_dev11: 'cassa-9-dev',
  s_rev11: 'th-23-review',
  s_dev9: 'cassa-9-dev',
  s_free1: 'free-permission',
  'm_a1b2c3d4~s_dev2': 'cassa-14-dev',
}

export const queued: QueuedAction[] = [{ id: 'q_71ab', kind: 'approve', projectId: 'p_9c0d1e2f', taskId: 'TH-23', planSha256: 'bb22', device: 'Galaxy S25', time: ago(9) }]

export const devices: Device[] = [{ id: 'd_s25', name: 'Galaxy S25', created: ago(60 * 24 * 2), lastSeen: ago(9), current: false }]

export const settings: Settings = {
  language: 'it',
  remote: { enabled: true, address: 'http://100.101.12.7:7420', mode: 'tailnet', devices: [{ id: 'd_s25', name: 'Galaxy S25', lastSeen: ago(9) }] },
  claude: { found: true, version: '2.4.1', path: '/usr/local/bin/claude' },
  gh: { found: true, authed: true },
  defaultPermissionMode: 'default',
  globalPluginInstalled: false,
  version: '0.9.0-dev',
  sessionsAvailable: true,
}

export const CONFIG_YAML = `version: 0
base: origin/main

commands:
  test: npm test
  analyze: npx eslint .

tasks:
  prefix: SITO

gates:
  approved:
    - check: plan_valid
    - check: human_approval
  verified:
    - check: test_proof
    - check: hand_passed
      with: { hand: analyze }
  reviewed:
    - check: review_by_other_role
  delivered:
    - check: evidence_fresh
    - check: conventional_commits
`

export const activations: Record<string, Activation> = {
  p_5e6f7a8b: {
    hasConfig: false,
    configYaml: CONFIG_YAML,
    stack: 'web',
    base: 'origin/main',
    gitHooks: { 'commit-msg': 'missing', 'pre-push': 'foreign' },
    configCommitted: false,
  },
}
