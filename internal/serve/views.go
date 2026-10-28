package serve

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spaghetticoders/truehand/internal/appstate"
	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/config"
	"github.com/spaghetticoders/truehand/internal/core"
	"github.com/spaghetticoders/truehand/internal/gate"
	"github.com/spaghetticoders/truehand/internal/gitx"
)

// The wire types of CONTRATTO-API.md: camelCase, computed here so the UI only draws.

type actorView struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"` // agent-dev | agent-review | agent | human
	Name   string `json:"name"`
	TaskID string `json:"taskId,omitempty"`
}

type clientView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type projectView struct {
	ID          string `json:"id"`
	MachineID   string `json:"machineId"`
	ClientID    string `json:"clientId"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	Key         string `json:"key"`
	Active      bool   `json:"active"`
	ConfigError string `json:"configError,omitempty"`
	Branch      string `json:"branch"`
}

type commentView struct {
	ID           string    `json:"id"`
	Time         time.Time `json:"time"`
	Actor        actorView `json:"actor"`
	Body         string    `json:"body"`
	OpenQuestion bool      `json:"openQuestion,omitempty"`
}

type checkView struct {
	ID     string `json:"id"`
	Gate   string `json:"gate"`
	Status string `json:"status"` // pass | fail | skip | pending
	Detail string `json:"detail,omitempty"`
}

type imageView struct {
	URL     string `json:"url"`
	Caption string `json:"caption"`
}

type proofView struct {
	ID      string      `json:"id"`
	Title   string      `json:"title"`
	Verdict string      `json:"verdict"`
	Summary string      `json:"summary"`
	Actor   actorView   `json:"actor"`
	Time    time.Time   `json:"time"`
	Fresh   bool        `json:"fresh"`
	Intact  bool        `json:"intact"`
	Images  []imageView `json:"images"`
	Log     string      `json:"log,omitempty"`
}

type diffFileView struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Status  string `json:"status"`
	InScope bool   `json:"inScope"`
	Binary  bool   `json:"binary,omitempty"`
}

type sessionView struct {
	ID             string     `json:"id"`
	MachineID      string     `json:"machineId"`
	TaskID         string     `json:"taskId,omitempty"`
	ProjectID      string     `json:"projectId"`
	Role           string     `json:"role"`
	Status         string     `json:"status"` // starting | working | waiting | idle | exited
	WaitingFor     string     `json:"waitingFor,omitempty"`
	StartedAt      time.Time  `json:"startedAt"`
	EndedAt        *time.Time `json:"endedAt,omitempty"`
	ExitCode       *int       `json:"exitCode,omitempty"`
	Resumable      bool       `json:"resumable"`
	StoppedByUser  bool       `json:"stoppedByUser,omitempty"`
	PermissionMode string     `json:"permissionMode"`
	Tail           []string   `json:"tail"`
}

type taskSummary struct {
	ID              string      `json:"id"`
	ProjectID       string      `json:"projectId"`
	Title           string      `json:"title"`
	State           board.State `json:"state"`
	Merged          bool        `json:"merged"`
	Pushed          bool        `json:"pushed"`
	PRURL           string      `json:"prUrl,omitempty"`
	Branch          string      `json:"branch,omitempty"`
	Worktree        string      `json:"worktree,omitempty"`
	PlanReady       bool        `json:"planReady"`
	HasOpenQuestion bool        `json:"hasOpenQuestion"`
	Failing         bool        `json:"failing"`
	Stale           bool        `json:"stale"`
	WaitReason      string      `json:"waitReason,omitempty"`
	PrimaryAction   string      `json:"primaryAction,omitempty"`
	ReadOnly        bool        `json:"readOnly"`
	UpdatedAt       time.Time   `json:"updatedAt"`
	SessionIDs      []string    `json:"sessionIds"`
}

type historyView struct {
	Time  time.Time   `json:"time"`
	Type  string      `json:"type"`
	Actor actorView   `json:"actor"`
	From  board.State `json:"from,omitempty"`
	To    board.State `json:"to,omitempty"`
}

type approvalView struct {
	Time    time.Time `json:"time"`
	Actor   actorView `json:"actor"`
	Via     string    `json:"via"`
	Current bool      `json:"current"`
}

type problemView struct {
	Section string `json:"section"`
	Message string `json:"message"`
}

type taskDetail struct {
	taskSummary
	Description    string         `json:"description"`
	ExternalTicket string         `json:"externalTicket,omitempty"`
	Scope          []string       `json:"scope"`
	Comments       []commentView  `json:"comments"`
	Plan           string         `json:"plan,omitempty"`
	PlanSHA256     string         `json:"planSha256,omitempty"`
	PlanProblems   []problemView  `json:"planProblems"`
	Checks         []checkView    `json:"checks"`
	Proofs         []proofView    `json:"proofs"`
	Diff           []diffFileView `json:"diff"`
	History        []historyView  `json:"history"`
	Approvals      []approvalView `json:"approvals"`
}

type queuedView struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	ProjectID  string `json:"projectId"`
	TaskID     string `json:"taskId"`
	PlanSHA256 string `json:"planSha256,omitempty"`
	// ConfigSHA256 names the config a queued commit-config would commit.
	ConfigSHA256 string    `json:"configSha256,omitempty"`
	Device       string    `json:"device"`
	Time         time.Time `json:"time"`
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// clientID is the client's name as a stable slug.
func clientID(name string) string {
	s := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if s == "" {
		return "none"
	}
	return s
}

func actorOf(id string, infos map[string]core.ActorInfo) actorView {
	a := actorView{ID: id, Name: id}
	info, ok := infos[id]
	if ok {
		a.Name, a.TaskID = info.Name, info.Task
	}
	switch {
	case ok && info.Kind == core.ActorHuman:
		a.Kind = "human"
	case ok && info.Kind == core.ActorAgent && info.Role == "dev":
		a.Kind = "agent-dev"
	case ok && info.Kind == core.ActorAgent && info.Role == "review":
		a.Kind = "agent-review"
	case strings.HasPrefix(id, "human:"):
		a.Kind = "human"
	default:
		a.Kind = "agent"
	}
	return a
}

// trustWait is how long a session may stay "starting" without any hook before it is shown as
// waiting for input: Claude Code asks whether to trust a new folder before any hook runs.
const trustWait = 20 * time.Second

// effective is the session as the human should see it.
func effective(x appstate.Session) appstate.Session {
	if x.Status == appstate.StatusStarting && time.Since(x.Started) > trustWait {
		x.Status = appstate.StatusWaitingInput
	}
	return x
}

func viewSession(x appstate.Session, tail []string) sessionView {
	x = effective(x)
	v := sessionView{ID: x.ID, MachineID: appstate.LocalMachine, TaskID: x.Task, ProjectID: x.Project, Role: x.Role, Status: x.Status, StartedAt: x.Started, EndedAt: x.Ended, ExitCode: x.ExitCode, Resumable: x.Resumable, StoppedByUser: x.Stopped, PermissionMode: x.PermissionMode, Tail: tail}
	switch x.Status {
	case appstate.StatusWaitingInput:
		v.Status, v.WaitingFor = "waiting", "input"
	case appstate.StatusWaitingPermission:
		v.Status, v.WaitingFor = "waiting", "permission"
	case appstate.StatusCrashed:
		v.Status = "exited"
	}
	if v.Tail == nil {
		v.Tail = []string{}
	}
	return v
}

func (s *Server) sessionViews(list []appstate.Session) []sessionView {
	out := []sessionView{}
	for _, x := range list {
		var tail []string
		if s.Agents != nil {
			tail = s.Agents.Tail(x.ID, 5)
		}
		out = append(out, viewSession(x, tail))
	}
	return out
}

func (s *Server) allSessions() []appstate.Session {
	var list []appstate.Session
	if s.Agents != nil {
		list, _ = s.Agents.Sessions()
	} else {
		list, _ = s.Store.Sessions()
	}
	if list == nil {
		list = []appstate.Session{}
	}
	return list
}

// projectData is one project read once: its board across worktrees and its sessions.
type projectData struct {
	P         appstate.Project
	Active    bool
	ConfigErr string
	Main      *core.Workspace
	PV        *core.ProjectView
	Sessions  []appstate.Session
	Branch    string
	Prefix    string
}

// boardCacheTTL bounds how long a cached board may miss edits outside .truehand (working
// tree files, which change evidence freshness).
const boardCacheTTL = 10 * time.Second

type cachedBoard struct {
	sig string
	at  time.Time
	pd  projectData
}

// load reads a project's board. Building it runs git for every task in every worktree, so
// it is cached until the project's .truehand trees or HEADs change, or for boardCacheTTL.
func (s *Server) load(p appstate.Project, sessions []appstate.Session) *projectData {
	sig := projectSignature(p.Root) + "|" + p.Client + "|" + p.Name
	s.mu.Lock()
	c, ok := s.boards[p.ID]
	s.mu.Unlock()
	var pd *projectData
	if ok && c.sig == sig && time.Since(c.at) < boardCacheTTL {
		cp := c.pd
		pd = &cp
	} else {
		pd = s.loadBoard(p)
		s.mu.Lock()
		if s.boards == nil {
			s.boards = map[string]cachedBoard{}
		}
		s.boards[p.ID] = cachedBoard{sig: sig, at: time.Now(), pd: *pd}
		s.mu.Unlock()
	}
	pd.Sessions = nil
	for _, x := range sessions {
		if x.Project == p.ID {
			pd.Sessions = append(pd.Sessions, x)
		}
	}
	return pd
}

// forget drops a project's cached board after a change made through this server.
func (s *Server) forget(pid string) {
	s.mu.Lock()
	delete(s.boards, pid)
	s.mu.Unlock()
}

func (s *Server) loadBoard(p appstate.Project) *projectData {
	pd := &projectData{P: p}
	if repo, err := gitx.Open(p.Root); err == nil {
		b, _ := repo.Git("symbolic-ref", "--short", "-q", "HEAD")
		pd.Branch = strings.TrimSpace(b)
	}
	if _, err := os.Stat(config.Path(p.Root)); err != nil {
		return pd
	}
	main, err := core.Open(p.Root)
	if err != nil {
		pd.ConfigErr = err.Error()
		return pd
	}
	pd.Main, pd.Prefix = main, main.Config.TaskPrefix()
	pv, err := main.ProjectBoard()
	if err != nil {
		pd.ConfigErr = err.Error()
		return pd
	}
	pd.Active, pd.PV = true, pv
	return pd
}

func (pd *projectData) view() projectView {
	v := projectView{ID: pd.P.ID, MachineID: appstate.LocalMachine, ClientID: clientID(pd.P.Client), Name: pd.P.Name, Path: pd.P.Root, Active: pd.Active, ConfigError: pd.ConfigErr, Branch: pd.Branch}
	if pd.Active {
		v.Key = pd.Prefix
	}
	return v
}

func (pd *projectData) stuckStarting(sid string) bool {
	for _, x := range pd.Sessions {
		if x.ID == sid {
			return x.Status == appstate.StatusStarting && effective(x).Status != x.Status
		}
	}
	return false
}

func (pd *projectData) taskSessions(id string) []appstate.Session {
	var out []appstate.Session
	for _, x := range pd.Sessions {
		if x.Task == id {
			out = append(out, effective(x))
		}
	}
	return out
}

// taskBranch is the branch a task's work is pushed from.
func (pd *projectData) taskBranch(pt core.ProjectTask) string {
	switch {
	case pt.Branch != "":
		return pt.Branch
	case pt.Worktree != "":
		return core.TaskBranch(pt.ID)
	}
	return pd.Branch
}

func (s *Server) summarize(pd *projectData, pt core.ProjectTask, in core.WaitingInput) taskSummary {
	sess := pd.taskSessions(pt.ID)
	branch := pd.taskBranch(pt)
	s.mu.Lock()
	pr, hasPR := s.prs[branch]
	s.mu.Unlock()
	in.Push.PROpen = hasPR && pr.State == "OPEN"
	kinds := map[string]bool{}
	for _, it := range core.WaitingFor([]core.WaitingInput{in}, sessionStates(sess)) {
		kinds[it.Kind] = true
	}
	f := taskFacts{
		State: pt.State, ReadOnly: pt.Branch != "", Merged: pt.Merged, Stale: pt.Stale && !pt.Merged,
		PlanReady: kinds[core.WaitPlanApproval], OpenQuestion: kinds[core.WaitCommentQuestion], Failing: kinds[core.WaitBlocked],
		DeliverGateOpen: pt.State == board.Reviewed && pt.Next == string(board.Delivered) && len(pt.Missing) == 0,
		ReviewDone:      pt.State == board.Verified && pt.Next == string(board.Reviewed) && len(pt.Missing) == 0,
		Pushed:          in.Push.HasUpstream && in.Push.Ahead == 0,
		PROpenOrMerged:  hasPR && (pr.State == "OPEN" || pr.State == "MERGED"),
	}
	var lastDev *appstate.Session
	ids := []string{}
	for i, x := range sess {
		ids = append(ids, x.ID)
		live := appstate.Live(x.Status)
		switch {
		// Stuck at the folder-trust prompt: the agent can do nothing until answered.
		case live && x.Status == appstate.StatusWaitingInput && pd.stuckStarting(x.ID):
			f.SessionWaiting = "permission"
		case live && x.Status == appstate.StatusWaitingPermission:
			f.SessionWaiting = "permission"
		case live && x.Status == appstate.StatusWaitingInput && f.SessionWaiting == "":
			f.SessionWaiting = "input"
		}
		if x.Role == "dev" {
			f.LiveDev = f.LiveDev || live
			if lastDev == nil || x.Started.After(lastDev.Started) {
				lastDev = &sess[i]
			}
		}
		if x.Role == "review" && live {
			f.LiveReview = true
		}
	}
	f.ResumableDev = !f.LiveDev && lastDev != nil && lastDev.Resumable
	reason, action := decide(f)
	sum := taskSummary{
		ID: pt.ID, ProjectID: pd.P.ID, Title: pt.Title, State: pt.State, Merged: pt.Merged, Pushed: f.Pushed,
		Branch: pt.Branch, Worktree: pt.Worktree, PlanReady: f.PlanReady, HasOpenQuestion: f.OpenQuestion,
		Failing: f.Failing, Stale: f.Stale, WaitReason: reason, PrimaryAction: action, ReadOnly: f.ReadOnly,
		UpdatedAt: pt.Updated, SessionIDs: ids,
	}
	if hasPR {
		sum.PRURL = pr.URL
	}
	return sum
}

func (s *Server) summaries(pd *projectData) []taskSummary {
	out := []taskSummary{}
	if pd.PV == nil {
		return out
	}
	for i, pt := range pd.PV.Tasks {
		out = append(out, s.summarize(pd, pt, pd.PV.Waiting[i]))
	}
	return out
}

// findTask is the board entry of a task: the live one, or the one read from branch.
func (pd *projectData) findTask(id, branch string) (int, bool) {
	if pd.PV == nil {
		return 0, false
	}
	for i, pt := range pd.PV.Tasks {
		if pt.ID == id && pt.Branch == branch {
			return i, true
		}
	}
	return 0, false
}

const proofLogLimit = 64 << 10

var imageExt = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".svg": true}

func fileURL(pid, id, rel string) string {
	parts := strings.Split(rel, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return "/api/projects/" + pid + "/tasks/" + id + "/files/" + strings.Join(parts, "/")
}

func (s *Server) detail(pd *projectData, sum taskSummary, d *core.TaskDetail, ws *core.Workspace, cfg *config.Config) taskDetail {
	t := d.Task
	infos := core.ResolveActors(core.ActorsOf(d), sessionRefs(s.allSessions()), ws.Identity())
	out := taskDetail{
		taskSummary: sum, Description: d.Description, ExternalTicket: t.Source, Scope: t.Scope,
		Comments: []commentView{}, PlanProblems: []problemView{}, Checks: []checkView{}, Proofs: []proofView{},
		Diff: []diffFileView{}, History: []historyView{}, Approvals: []approvalView{},
	}
	if out.Scope == nil {
		out.Scope = []string{}
	}
	lastAgent := -1
	for i, c := range d.Comments {
		text, _ := c.Detail["text"].(string)
		out.Comments = append(out.Comments, commentView{ID: commentID(c, i), Time: c.Time, Actor: actorOf(c.Actor, infos), Body: text})
		if !strings.HasPrefix(c.Actor, "human:") {
			lastAgent = i
		} else {
			lastAgent = -1
		}
	}
	if sum.HasOpenQuestion && lastAgent >= 0 {
		out.Comments[lastAgent].OpenQuestion = true
	}
	if d.Plan.Exists {
		out.Plan, out.PlanSHA256 = d.Plan.Markdown, d.Plan.SHA256
		for _, p := range d.Plan.Problems {
			out.PlanProblems = append(out.PlanProblems, problemView{Section: p.Section, Message: p.Message})
		}
	}
	out.Checks = checks(t.State, d.NextGate, cfg)
	for _, ev := range d.Evidence {
		pv := proofView{ID: ev.ID, Title: ev.Hand, Verdict: ev.Verdict, Summary: readableActors(ev.Summary, infos), Actor: actorOf(ev.Actor, infos), Time: ev.Created, Fresh: ev.Fresh, Intact: ev.Intact, Images: []imageView{}}
		for _, f := range ev.Files {
			ext := strings.ToLower(path.Ext(f.Path))
			switch {
			case imageExt[ext]:
				pv.Images = append(pv.Images, imageView{URL: fileURL(pd.P.ID, t.ID, f.Path), Caption: path.Base(f.Path)})
			case (ext == ".log" || ext == ".txt") && pv.Log == "" && sum.Branch == "":
				pv.Log = readLimited(ws.Store.TaskDir(t.ID), f.Path, proofLogLimit)
			}
		}
		out.Proofs = append(out.Proofs, pv)
	}
	for _, e := range d.Events {
		out.History = append(out.History, historyView{Time: e.Time, Type: e.Type, Actor: actorOf(e.Actor, infos), From: e.From, To: e.To})
	}
	for _, a := range d.Approvals {
		out.Approvals = append(out.Approvals, approvalView{Time: a.Time, Actor: actorOf("human:"+a.Email, infos), Via: a.Via, Current: d.Plan.Exists && a.PlanSHA == d.Plan.SHA256})
	}
	if sum.Branch == "" {
		if df, err := ws.Diff(t, "", -1); err == nil { // files only
			for _, f := range df.Files {
				out.Diff = append(out.Diff, diffFileView{Path: f.Path, Added: f.Add, Removed: f.Del, Status: f.Status, InScope: f.InScope, Binary: f.Binary})
			}
		}
	}
	return out
}

// readableActors replaces actor ids in text the hands wrote ("review pass by claude:<uuid>")
// with what a person recognises.
func readableActors(text string, infos map[string]core.ActorInfo) string {
	for id, info := range infos {
		if !strings.Contains(text, id) {
			continue
		}
		label := info.Name
		switch {
		case info.Kind == core.ActorAgent && info.Role == "dev":
			label = "the dev agent"
		case info.Kind == core.ActorAgent && info.Role == "review":
			label = "the review agent"
		case info.Kind == core.ActorAgent || info.Kind == core.ActorAgentUnbound:
			label = "an agent"
		}
		text = strings.ReplaceAll(text, id, label)
	}
	return text
}

func commentID(e board.Event, i int) string {
	return "c" + strconv.Itoa(i)
}

func readLimited(dir, rel string, limit int64) string {
	p, err := safeJoin(dir, rel)
	if err != nil {
		return ""
	}
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, limit))
	return string(b)
}

// checks are the next gate's results, then the later gates' checks as pending.
func checks(state board.State, next *gate.Result, cfg *config.Config) []checkView {
	out := []checkView{}
	if next != nil {
		for _, it := range next.Items {
			out = append(out, checkView{ID: it.Check, Gate: next.Target, Status: string(it.Status), Detail: it.Message})
		}
	}
	if cfg == nil {
		return out
	}
	later := state.Next()
	if later != "" {
		later = later.Next()
	}
	for ; later != ""; later = later.Next() {
		for _, r := range cfg.Gates[string(later)] {
			out = append(out, checkView{ID: r.Check, Gate: string(later), Status: "pending"})
		}
	}
	return out
}

func (s *Server) queuedViews() []queuedView {
	out := []queuedView{}
	for _, q := range s.queuedActions() {
		cfg, _ := q.Params["configSha256"].(string)
		out = append(out, queuedView{ID: q.ID, Kind: q.Kind, ProjectID: q.Project, TaskID: q.Task, PlanSHA256: q.PlanSHA256, ConfigSHA256: cfg, Device: q.Device, Time: q.Time})
	}
	return out
}

type meView struct {
	Via        string `json:"via"`
	CanApprove bool   `json:"canApprove"`
	DeviceName string `json:"deviceName,omitempty"`
	ReadOnly   bool   `json:"readOnly"`
}

func (s *Server) meOf(r *http.Request) meView {
	id := identityOf(r)
	m := meView{Via: id.Via, CanApprove: id.Shell, ReadOnly: s.ReadOnly || id.ViewOnly}
	if id.Device != nil {
		m.DeviceName = id.Device.Name
	}
	return m
}

type snapshotView struct {
	Machines []machineView `json:"machines,omitempty"`
	Clients  []clientView  `json:"clients"`
	Projects []projectView `json:"projects"`
	Tasks    []taskSummary `json:"tasks"`
	Sessions []sessionView `json:"sessions"`
	Settings settingsView  `json:"settings"`
	Me       meView        `json:"me"`
	Queued   []queuedView  `json:"queued"`
}

func (s *Server) snapshotOf(r *http.Request) (*snapshotView, error) {
	st, err := s.Store.State()
	if err != nil {
		return nil, err
	}
	settings, err := s.settingsView(r.Context())
	if err != nil {
		return nil, err
	}
	sessions := s.allSessions()
	snap := &snapshotView{Clients: []clientView{}, Projects: []projectView{}, Tasks: []taskSummary{}, Sessions: s.sessionViews(sessions), Settings: settings, Me: s.meOf(r), Queued: s.queuedViews()}
	seen := map[string]bool{}
	for _, p := range st.Projects {
		pd := s.load(p, sessions)
		pv := pd.view()
		if !seen[pv.ClientID] {
			seen[pv.ClientID] = true
			snap.Clients = append(snap.Clients, clientView{ID: pv.ClientID, Name: p.Client})
		}
		snap.Projects = append(snap.Projects, pv)
		snap.Tasks = append(snap.Tasks, s.summaries(pd)...)
	}
	if !s.Peer {
		s.addRemotes(r.Context(), snap)
	}
	sort.SliceStable(snap.Clients, func(i, j int) bool { return snap.Clients[i].Name < snap.Clients[j].Name })
	return snap, nil
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	snap, err := s.snapshotOf(r)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, snap)
}

// waitingCount is how many tasks wait on the human, plus the actions queued for the Mac.
func (s *Server) waitingCount() int {
	st, err := s.Store.State()
	if err != nil {
		return 0
	}
	sessions := s.allSessions()
	n := len(s.queuedActions())
	for _, p := range st.Projects {
		for _, t := range s.summaries(s.load(p, sessions)) {
			if t.WaitReason != "" {
				n++
			}
		}
	}
	return n
}
