// Wire types of the Truehand server (CONTRATTO-API.md, internal/serve/views.go). Field names match the JSON.

export type TaskState = 'planned' | 'approved' | 'building' | 'verified' | 'reviewed' | 'delivered'

export const TASK_STATES: TaskState[] = ['planned', 'approved', 'building', 'verified', 'reviewed', 'delivered']

export type ActorKind = 'agent-dev' | 'agent-review' | 'agent-plan' | 'agent' | 'human'

/** id is raw ("claude:<uuid>", "human:<email>") and never shown. */
export interface Actor {
  id: string
  kind: ActorKind
  name: string
  taskId?: string
}

export interface Client {
  id: string
  name: string
}

export interface Project {
  id: string
  clientId: string
  name: string
  path: string
  key: string
  active: boolean
  /** Config present but broken: the project is blocked, not "to activate". */
  configError?: string
  branch: string
  machineId?: string
}

export interface Comment {
  id: string
  time: string
  actor: Actor
  body: string
  openQuestion?: boolean
}

export type CheckStatus = 'pass' | 'fail' | 'skip' | 'pending'

/** id is the gate check name; the UI translates it. detail is the server's technical message. */
export interface Check {
  id: string
  gate: TaskState
  status: CheckStatus
  detail?: string
}

export interface Proof {
  id: string
  /** The hand: test-proof, eyes, review, attach or a custom name. */
  title: string
  verdict: 'pass' | 'fail'
  summary: string
  actor: Actor
  time: string
  fresh: boolean
  intact: boolean
  images: { url: string; caption: string }[]
  log?: string
}

export interface DiffFile {
  path: string
  added: number
  removed: number
  status: 'A' | 'M' | 'D' | 'R' | 'T'
  inScope: boolean
  binary?: boolean
}

export interface FilePatch {
  path: string
  patch: string
  truncated: boolean
}

export type SessionRole = 'dev' | 'review' | 'plan' | 'free'

export type SessionStatus = 'starting' | 'working' | 'waiting' | 'idle' | 'exited'

export type PermissionMode = 'default' | 'acceptEdits' | 'plan'

export const PERMISSION_MODES: PermissionMode[] = ['default', 'acceptEdits', 'plan']

export interface Session {
  id: string
  taskId?: string
  projectId: string
  role: SessionRole
  status: SessionStatus
  waitingFor?: 'input' | 'permission' | 'trust'
  startedAt: string
  endedAt?: string
  exitCode?: number
  /** Closed from the app: never an error, whatever the exit code. */
  stoppedByUser?: boolean
  resumable: boolean
  permissionMode: PermissionMode
  /** Last 5 lines, ANSI removed. */
  tail: string[]
  machineId?: string
}

export type WaitReason = 'approvePlan' | 'question' | 'permission' | 'review' | 'deliver' | 'push' | 'failing' | 'stale'

export type TaskAction =
  | 'activate'
  | 'start-agent'
  | 'approve-plan'
  | 'start-review'
  | 'deliver'
  | 'push'
  | 'open-pr'
  | 'open-agent'
  | 'send-back'
  | 'resume-agent'

export interface TaskSummary {
  id: string
  projectId: string
  title: string
  state: TaskState
  merged: boolean
  pushed: boolean
  prUrl?: string
  /** Set when the task is read from another branch: read-only. */
  branch?: string
  worktree?: string
  planReady: boolean
  hasOpenQuestion: boolean
  failing: boolean
  stale: boolean
  waitReason?: WaitReason
  primaryAction?: TaskAction
  readOnly: boolean
  updatedAt: string
  sessionIds: string[]
}

export interface TaskDetail extends TaskSummary {
  description: string
  externalTicket?: string
  scope: string[]
  comments: Comment[]
  plan?: string
  planSha256?: string
  planProblems: { section: string; message: string }[]
  checks: Check[]
  proofs: Proof[]
  diff: DiffFile[]
  history: { time: string; type: string; actor: Actor; from?: TaskState; to?: TaskState }[]
  approvals: { time: string; actor: Actor; via: string; current: boolean }[]
}

export type RemoteMode = 'tailnet' | 'tailscale-serve'

export interface Settings {
  language: 'it' | 'en'
  remote: {
    enabled: boolean
    address: string
    mode: RemoteMode
    devices: { id: string; name: string; lastSeen: string }[]
    /** What is really running, which may differ from what was asked. */
    status?: 'off' | 'listening' | 'error'
    error?: string
  }
  claude: { found: boolean; version?: string; path?: string }
  gh: { found: boolean; authed: boolean }
  defaultPermissionMode: PermissionMode
  globalPluginInstalled: boolean
  version: string
  sessionsAvailable: boolean
}

export type SettingsPatch = Partial<Pick<Settings, 'language' | 'defaultPermissionMode'>> & {
  remote?: Partial<Pick<Settings['remote'], 'enabled' | 'address' | 'mode'>>
}

export interface Me {
  via: 'token' | 'device'
  /** True only in the Mac app, which carries the human factor. */
  canApprove: boolean
  deviceName?: string
  readOnly: boolean
}

export interface QueuedAction {
  id: string
  kind: 'approve' | 'push' | 'pr' | 'config' | 'commit-config'
  projectId: string
  taskId: string
  planSha256?: string
  /** commit-config: the config the request was made for. */
  configSha256?: string
  device: string
  time: string
}

export type MachineStatus = 'online' | 'offline' | 'needs-update' | 'needs-auth' | 'disabled'

export interface Machine {
  id: string
  label: string
  sshTarget?: string
  os?: 'darwin' | 'linux'
  arch?: string
  status: MachineStatus
  version?: string
  tools?: { claude: boolean; git: boolean; gh: boolean }
  canApprove: boolean
  error?: string
}

export interface Snapshot {
  clients: Client[]
  projects: Project[]
  tasks: TaskSummary[]
  sessions: Session[]
  settings: Settings
  me: Me
  queued: QueuedAction[]
  machines?: Machine[]
}

export type HookState = 'ours' | 'foreign' | 'missing'

export interface Activation {
  hasConfig: boolean
  configYaml: string
  configError?: string
  stack: string
  base: string
  gitHooks: Record<string, HookState>
  configCommitted: boolean
  /** Names the config shown: commit-config commits only that content. */
  configSha256?: string
  /** Unified diff of the config against the base branch. */
  configDiff?: string
  approvers?: ApproverRef[]
  approversAdded?: ApproverRef[]
  approversRemoved?: ApproverRef[]
}

/** key is the first 12 hex of the public key's sha256: the name is whatever the config says. */
export interface ApproverRef {
  id: string
  name: string
  key: string
}

export interface Device {
  id: string
  name: string
  created: string
  lastSeen: string
  current: boolean
}

export interface DirEntry {
  name: string
  path: string
  isGit: boolean
}

export interface NewTask {
  title: string
  description?: string
  externalTicket?: string
  scope?: string[]
  id?: string
  worktree?: boolean
}

export interface ActionArgs {
  planSha256?: string
  reason?: string
  state?: TaskState
  sessionId?: string
  permissionMode?: PermissionMode
  signedIntent?: SignedIntent
}

export interface SignedIntent {
  intent: unknown
  format: 'es256'
  sig: string
}

export interface ActResult {
  ok: boolean
  sessionId?: string
  url?: string
  queued?: boolean
  queuedId?: string
  gate?: Check[]
  output?: string
  error?: string
  warning?: string
}

export type ServerEvent =
  | { type: 'changed'; project: string }
  | { type: 'session'; id: string; status: SessionStatus; waitingFor?: Session['waitingFor'] }
  | { type: 'waiting'; count: number }
  | { type: 'machine'; id: string; status: MachineStatus }

export interface PtyHandlers {
  onHello?: (h: { cols: number; rows: number; status: SessionStatus; waitingFor?: string }) => void
  /** Binary frames go to xterm as they are: converting them would split UTF-8. */
  onData: (chunk: Uint8Array | string) => void
  onExit?: (e: { status: SessionStatus; code?: number }) => void
}

export interface PtyConnection {
  send(data: string): void
  resize(cols: number, rows: number): void
  close(): void
}

export interface Api {
  snapshot(): Promise<Snapshot>
  /** Inside the Mac app: who we are with the shell's human factor. */
  shellMe(): Promise<Me | null>
  login(token: string): Promise<void>
  pair(code: string, deviceName: string): Promise<void>
  pairCode(): Promise<{ code: string; expires: string }>
  devices(): Promise<Device[]>
  deleteDevice(id: string): Promise<void>
  rotateToken(): Promise<void>
  updateSettings(s: SettingsPatch): Promise<Settings>

  addProject(p: { path: string; clientName: string; name?: string; machineId?: string }): Promise<Project>
  patchProject(pid: string, p: { clientName?: string; name?: string }): Promise<Project>
  removeProject(pid: string): Promise<void>
  fsDirs(path: string, machineId?: string): Promise<DirEntry[]>
  /** Native folder dialog of the Mac app, or null when not in it. */
  pickFolder(): Promise<string | null> | null
  /** Touch ID signature by the Mac app for repos with approvers, or null outside it. */
  signIntent(projectId: string, taskId: string, action: TaskAction, approver?: string): Promise<SignedIntent> | null
  activation(pid: string): Promise<Activation>
  activate(pid: string, o: { stack?: string; base?: string; forceHooks?: boolean }): Promise<Activation>
  /** From a phone the change is queued for the Mac. */
  putConfig(pid: string, yaml: string): Promise<Activation | { queued: true; queuedId?: string }>
  /** Commits the config with that hash on the base branch (Touch ID on the Mac, queued from a phone). */
  commitConfig(pid: string, configSha256: string): Promise<ActResult>
  /** This Mac's approver key fingerprint, null without a key, or null outside the Mac app. */
  macKeyFingerprint(): Promise<string | null> | null
  fetchRemote(pid: string): Promise<{ ok: boolean; output: string }>

  task(pid: string, id: string, branch?: string): Promise<TaskDetail>
  filePatch(pid: string, id: string, path: string): Promise<FilePatch>
  createTask(pid: string, t: NewTask): Promise<TaskSummary & { ticketFetchError?: string }>
  editTask(pid: string, id: string, p: { title?: string; description?: string; externalTicket?: string }): Promise<TaskSummary>
  addComment(pid: string, id: string, body: string, sendToSession?: string): Promise<Comment>
  act(pid: string, id: string, action: TaskAction, args?: ActionArgs): Promise<ActResult>
  deleteWorktree(pid: string, id: string, force?: boolean): Promise<void>
  confirmQueued(qid: string, signedIntent?: SignedIntent): Promise<ActResult>
  discardQueued(qid: string): Promise<void>

  startFreeSession(pid: string, o?: { permissionMode?: PermissionMode; cols?: number; rows?: number }): Promise<Session>
  resumeSession(sid: string, o?: { cols?: number; rows?: number }): Promise<Session>
  signalSession(sid: string, sig: 'int' | 'term'): Promise<void>
  deleteSession(sid: string): Promise<void>
  pty(sid: string, h: PtyHandlers): PtyConnection

  addMachine(sshTarget: string, label?: string): Promise<Machine>
  reconnectMachine(mid: string): Promise<Machine>
  updateMachine(mid: string): Promise<Machine>
  removeMachine(mid: string): Promise<void>

  events(onEvent: (e: ServerEvent) => void): () => void
}
