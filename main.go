// herdr-goto: a small tree-style switcher across repos, worktrees and panes.
//
// It talks to herdr through its CLI (workspace list / pane list to read,
// workspace focus / agent focus to act). Designed to run inside a herdr pane
// (type = "pane" keybind), full screen, single shot: open, pick, exit.
//
// The hierarchy (repos -> worktrees/panes) and the filter-that-keeps-ancestors
// are custom (no off-the-shelf tree component fits). The generic parts lean on
// the official bubbles: textinput (search box), viewport (scroll), key + help.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"
)

// ---- herdr CLI JSON shapes ----

type worktree struct {
	RepoKey      string `json:"repo_key"`
	RepoName     string `json:"repo_name"`
	RepoRoot     string `json:"repo_root"`
	CheckoutPath string `json:"checkout_path"`
	IsLinked     bool   `json:"is_linked_worktree"`
}

type wsInfo struct {
	ID       string    `json:"workspace_id"`
	Label    string    `json:"label"`
	Number   int       `json:"number"`
	Worktree *worktree `json:"worktree"`
}

type paneInfo struct {
	ID          string `json:"pane_id"`
	WsID        string `json:"workspace_id"`
	Agent       string `json:"agent"`
	AgentStatus string `json:"agent_status"`
	Cwd         string `json:"cwd"`
}

type wsResp struct {
	Result struct {
		Workspaces []wsInfo `json:"workspaces"`
	} `json:"result"`
}

type paneResp struct {
	Result struct {
		Panes []paneInfo `json:"panes"`
	} `json:"result"`
}

// ---- tree ----

type node struct {
	kind     string // "repo" | "worktree" | "pane"
	label    string // own display text (matched against the query)
	branch   string // git branch of the checkout; extra search text (labels are folder slugs, branches keep their "/")
	ticket   string // normalized Jira key ("FED-2030") from branch/label, or the PR title as fallback
	ghSlug   string // "owner/repo" of the GitHub origin; "" when non-GitHub/unknown
	pr       *prRef // PR for this branch, annotated from cache/gh; nil until known or when none
	tktW     int    // ticket column width within the sibling group (0 = no prefix)
	prW      int    // "#123" column width within the sibling group (0 = no PR column)
	path     string // breadcrumb, e.g. "monorepo-front › infra-metrics"
	wsID     string // workspace to focus (repo/worktree, and a pane's workspace)
	paneID   string // pane to focus
	status   string // aggregated agent status (see statusDot); drives the gutter dot
	expanded bool
	children []*node
}

func herdrBin() string {
	if b := os.Getenv("HERDR_BIN_PATH"); b != "" {
		return b
	}
	return "herdr"
}

// ---- persisted UI state ----

type persisted struct {
	ShowPanes bool `json:"show_panes"`
}

func stateFile() string {
	// Running as a herdr plugin: herdr creates and injects a per-plugin state
	// dir; runtime state must live there, not in the plugin checkout.
	if dir := os.Getenv("HERDR_PLUGIN_STATE_DIR"); dir != "" {
		return filepath.Join(dir, "state.json")
	}
	// Standalone fallback (fixed-path install at ~/.config/herdr/goto-tui).
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		if h, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(h, ".config")
		}
	}
	return filepath.Join(base, "herdr", "goto-tui", "state.json")
}

func loadState() persisted {
	var s persisted
	if data, err := os.ReadFile(stateFile()); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	return s
}

func saveStateCmd(s persisted) tea.Cmd {
	return func() tea.Msg {
		if data, err := json.Marshal(s); err == nil {
			path := stateFile()
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
			_ = os.WriteFile(path, data, 0o644)
		}
		return nil
	}
}

// ---- GitHub PR info (via gh, async) ----

// prRef is the PR shown next to a branch. Ticket is extracted from the PR
// title and used only when the branch/label carry no ticket themselves.
type prRef struct {
	Number int    `json:"number"`
	State  string `json:"state"` // "open" | "draft" | "merged" | "closed"
	Ticket string `json:"ticket,omitempty"`
}

// ghPR mirrors one item of `gh pr list --json number,headRefName,state,isDraft,title`.
type ghPR struct {
	Number      int    `json:"number"`
	HeadRefName string `json:"headRefName"`
	State       string `json:"state"` // OPEN | MERGED | CLOSED
	IsDraft     bool   `json:"isDraft"`
	Title       string `json:"title"`
}

func (p ghPR) ref() prRef {
	state := strings.ToLower(p.State)
	if p.IsDraft && p.State == "OPEN" {
		state = "draft"
	}
	return prRef{Number: p.Number, State: state, Ticket: ticketFrom(p.Title)}
}

type repoPRs struct {
	FetchedAt time.Time        `json:"fetched_at"`
	Branches  map[string]prRef `json:"branches"`
}

// prCache is the stale-while-revalidate disk cache of branch -> PR per repo:
// cached entries render immediately on startup while gh refreshes them in the
// background, so PR info is visible even in this tool's open-pick-exit
// lifetime.
type prCache struct {
	Repos map[string]repoPRs `json:"repos"`
}

// prCacheFresh is how recent a repo's cached PRs must be to skip the
// background refresh. It only debounces rapid reopen cycles; older entries
// are still shown immediately while they revalidate.
const prCacheFresh = 60 * time.Second

func prCacheFile() string {
	return filepath.Join(filepath.Dir(stateFile()), "prcache.json")
}

func loadPRCache() prCache {
	var c prCache
	if data, err := os.ReadFile(prCacheFile()); err == nil {
		_ = json.Unmarshal(data, &c)
	}
	if c.Repos == nil {
		c.Repos = map[string]repoPRs{}
	}
	return c
}

func savePRCacheCmd(c prCache) tea.Cmd {
	return func() tea.Msg {
		if data, err := json.Marshal(c); err == nil {
			path := prCacheFile()
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
			_ = os.WriteFile(path, data, 0o644)
		}
		return nil
	}
}

// repoPRsMsg delivers one repo's branch -> PR mapping fetched from gh. err
// leaves nodes and cache untouched (missing gh, network down, non-GitHub);
// degradation is silent by design.
type repoPRsMsg struct {
	slug     string
	byBranch map[string]prRef
	err      bool
}

// fetchRepoPRsCmd resolves the PR of each local branch with one
// `gh pr list --head <branch>` call per branch, run in parallel. A repo-wide
// listing would miss older PRs in busy shared repos, where the newest N PRs
// are mostly other people's. prev (the previously cached mapping) fills in
// branches whose lookup failed transiently, so an error never wipes a valid
// cached PR; only when every branch fails is the whole message marked err.
func fetchRepoPRsCmd(slug string, branches []string, prev map[string]prRef) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		type result struct {
			pr  *prRef
			err bool
		}
		results := make([]result, len(branches))
		var wg sync.WaitGroup
		for i, branch := range branches {
			wg.Add(1)
			go func(i int, branch string) {
				defer wg.Done()
				out, err := exec.CommandContext(ctx, "gh", "pr", "list", "--repo", slug,
					"--head", branch, "--state", "all",
					"--json", "number,headRefName,state,isDraft,title",
					"--limit", "5").Output()
				if err != nil {
					results[i] = result{err: true}
					return
				}
				var prs []ghPR
				if json.Unmarshal(out, &prs) != nil {
					results[i] = result{err: true}
					return
				}
				if len(prs) > 0 {
					p := prs[0].ref() // newest first
					results[i] = result{pr: &p}
				}
			}(i, branch)
		}
		wg.Wait()

		byBranch := map[string]prRef{}
		errs := 0
		for i, r := range results {
			switch {
			case r.err:
				errs++
				if p, ok := prev[branches[i]]; ok {
					byBranch[branches[i]] = p
				}
			case r.pr != nil:
				byBranch[branches[i]] = *r.pr
			}
		}
		if errs == len(branches) {
			return repoPRsMsg{slug: slug, err: true}
		}
		return repoPRsMsg{slug: slug, byBranch: byBranch}
	}
}

// annotatePRs applies a branch -> PR mapping to one repo's nodes, clearing
// entries whose branch no longer has a PR and falling back to the PR-title
// ticket when branch/label yielded none.
func annotatePRs(nodes []*node, byBranch map[string]prRef) {
	for _, n := range nodes {
		p, ok := byBranch[n.branch]
		if !ok {
			n.pr = nil
			continue
		}
		pp := p
		n.pr = &pp
		if n.ticket == "" && p.Ticket != "" {
			n.ticket = p.Ticket
		}
	}
}

func loadJSON(args []string, out any) error {
	data, err := exec.Command(herdrBin(), args...).Output()
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func homeRel(p string) string {
	if h, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, h) {
		return "~" + strings.TrimPrefix(p, h)
	}
	return p
}

// resolveGitDir returns the git dir of the checkout at path, handling both
// main checkouts (.git dir) and linked worktrees (.git file with a "gitdir:"
// pointer). Returns "" for non-repos. Reads the filesystem directly (no
// subprocess; a `git` call per workspace would slow startup).
func resolveGitDir(path string) string {
	if path == "" {
		return ""
	}
	gitdir := filepath.Join(path, ".git")
	if fi, err := os.Stat(gitdir); err != nil {
		return ""
	} else if !fi.IsDir() {
		data, err := os.ReadFile(gitdir)
		if err != nil {
			return ""
		}
		target := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir:"))
		if target == "" {
			return ""
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(path, target)
		}
		gitdir = target
	}
	return gitdir
}

// gitBranch resolves the branch checked out at path by reading .git/HEAD
// directly. Returns "" for detached HEAD or non-repos.
func gitBranch(path string) string {
	gitdir := resolveGitDir(path)
	if gitdir == "" {
		return ""
	}
	head, err := os.ReadFile(filepath.Join(gitdir, "HEAD"))
	if err != nil {
		return ""
	}
	ref := strings.TrimSpace(string(head))
	if b, ok := strings.CutPrefix(ref, "ref: refs/heads/"); ok {
		return b
	}
	return "" // detached HEAD
}

// worktreeCreatedAt returns when the checkout at path was created, using the
// directory's birth time (darwin). Zero time when the path can't be stat'ed,
// which sorts those entries first.
func worktreeCreatedAt(path string) time.Time {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec)
	}
	return fi.ModTime()
}

// ticketRe matches a Jira-style ticket key: a project key of 2+ letters, a
// dash and digits (FED-2030, plat-1193). Letters-only before the dash keeps
// slugs like "e2e" or "v1-2" from matching.
var ticketRe = regexp.MustCompile(`(?i)\b([a-z][a-z]+-[0-9]+)\b`)

// ticketFrom extracts a normalized (uppercase) ticket key from the first
// source that contains one. Returns "" when none matches.
func ticketFrom(sources ...string) string {
	for _, s := range sources {
		if m := ticketRe.FindString(s); m != "" {
			return strings.ToUpper(m)
		}
	}
	return ""
}

// githubSlug resolves "owner/repo" from the origin remote of the repo at
// repoRoot, reading the git config file directly (startup-safe, no
// subprocess). Returns "" when the origin is missing or not on github.com.
func githubSlug(repoRoot string) string {
	gitdir := resolveGitDir(repoRoot)
	if gitdir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(gitdir, "config"))
	if err != nil {
		// A linked-worktree gitdir keeps config in the common dir.
		common, cerr := os.ReadFile(filepath.Join(gitdir, "commondir"))
		if cerr != nil {
			return ""
		}
		target := strings.TrimSpace(string(common))
		if !filepath.IsAbs(target) {
			target = filepath.Join(gitdir, target)
		}
		if data, err = os.ReadFile(filepath.Join(target, "config")); err != nil {
			return ""
		}
	}
	return githubSlugFromURL(originURL(string(data)))
}

// originURL scans git config content for the url of [remote "origin"].
func originURL(config string) string {
	inOrigin := false
	for _, line := range strings.Split(config, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			inOrigin = line == `[remote "origin"]`
			continue
		}
		if !inOrigin {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "url"); ok {
			rest = strings.TrimSpace(rest)
			if v, ok := strings.CutPrefix(rest, "="); ok {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

// githubSlugFromURL extracts "owner/repo" from the ssh/https GitHub remote
// URL forms. Non-GitHub hosts return "".
func githubSlugFromURL(url string) string {
	var rest string
	switch {
	case strings.HasPrefix(url, "git@github.com:"):
		rest = strings.TrimPrefix(url, "git@github.com:")
	case strings.HasPrefix(url, "ssh://git@github.com/"):
		rest = strings.TrimPrefix(url, "ssh://git@github.com/")
	case strings.HasPrefix(url, "https://github.com/"):
		rest = strings.TrimPrefix(url, "https://github.com/")
	default:
		return ""
	}
	rest = strings.TrimSuffix(strings.TrimSuffix(rest, "/"), ".git")
	if strings.Count(rest, "/") != 1 {
		return ""
	}
	return rest
}

func paneLeaf(p paneInfo) string {
	name := p.Agent
	status := ""
	if name != "" {
		status = "  [" + p.AgentStatus + "]"
	} else {
		name = "shell"
	}
	return name + status + "  " + filepath.Base(homeRel(p.Cwd))
}

// paneStatus is the agent status used for the gutter dot. Shell panes (no agent)
// carry no status so they don't pull a workspace's aggregate toward a stale dot.
func paneStatus(p paneInfo) string {
	if p.Agent == "" {
		return ""
	}
	return p.AgentStatus
}

// statusRank orders agent statuses when aggregating a workspace's panes,
// mirroring herdr's workspace_attention_priority: blocked beats done, done beats
// working, working beats idle, idle beats none/unknown.
func statusRank(s string) int {
	switch s {
	case "blocked":
		return 4
	case "done":
		return 3
	case "working":
		return 2
	case "idle":
		return 1
	default: // "unknown", "" (shell / no agent)
		return 0
	}
}

// aggregateStatus sets n.status to the highest-priority status among n and its
// descendants, so a repo/worktree reflects its panes' state even when panes are
// hidden. Returns the resolved status for the recursion.
func aggregateStatus(n *node) string {
	best := n.status
	for _, c := range n.children {
		if s := aggregateStatus(c); statusRank(s) > statusRank(best) {
			best = s
		}
	}
	n.status = best
	return best
}

func buildTree(wss []wsInfo, panes []paneInfo) []*node {
	byWs := map[string][]paneInfo{}
	for _, p := range panes {
		byWs[p.WsID] = append(byWs[p.WsID], p)
	}

	paneNodes := func(wsID, parentPath string) []*node {
		var out []*node
		for _, p := range byWs[wsID] {
			leaf := paneLeaf(p)
			out = append(out, &node{
				kind: "pane", label: leaf, path: parentPath + " › " + leaf,
				wsID: wsID, paneID: p.ID, status: paneStatus(p), expanded: true,
			})
		}
		return out
	}

	// Group workspaces into repos.
	type group struct {
		key  string
		wss  []wsInfo
		minN int
	}
	order := []string{}
	groups := map[string]*group{}
	for _, ws := range wss {
		key := ws.ID
		if ws.Worktree != nil {
			switch {
			case ws.Worktree.RepoKey != "":
				key = ws.Worktree.RepoKey
			case ws.Worktree.CheckoutPath != "":
				key = ws.Worktree.CheckoutPath
			}
		} else if ps := byWs[ws.ID]; len(ps) > 0 {
			key = ps[0].Cwd
		}
		g := groups[key]
		if g == nil {
			g = &group{key: key, minN: 1 << 30}
			groups[key] = g
			order = append(order, key)
		}
		g.wss = append(g.wss, ws)
		if ws.Number < g.minN {
			g.minN = ws.Number
		}
	}
	glist := make([]*group, 0, len(order))
	for _, k := range order {
		glist = append(glist, groups[k])
	}
	sort.SliceStable(glist, func(i, j int) bool { return glist[i].minN < glist[j].minN })

	slugCache := map[string]string{}
	slugFor := func(root string) string {
		if s, ok := slugCache[root]; ok {
			return s
		}
		s := githubSlug(root)
		slugCache[root] = s
		return s
	}

	repoName := func(ws wsInfo) string {
		if ws.Worktree != nil && ws.Worktree.RepoName != "" {
			return ws.Worktree.RepoName
		}
		return ws.Label
	}
	isMain := func(ws wsInfo) bool {
		return ws.Worktree == nil || !ws.Worktree.IsLinked
	}

	var roots []*node
	for _, g := range glist {
		var main wsInfo
		found := false
		for _, ws := range g.wss {
			if isMain(ws) {
				main = ws
				found = true
				break
			}
		}
		if !found {
			main = g.wss[0]
		}
		name := repoName(main)
		repo := &node{kind: "repo", label: name, path: name, wsID: main.ID, expanded: true}
		if main.Worktree != nil {
			repo.branch = gitBranch(main.Worktree.CheckoutPath)
			repo.ticket = ticketFrom(repo.branch)
			repo.ghSlug = slugFor(main.Worktree.RepoRoot)
		}
		repo.children = append(repo.children, paneNodes(main.ID, name)...)

		others := []wsInfo{}
		for _, ws := range g.wss {
			if ws.ID != main.ID {
				others = append(others, ws)
			}
		}
		// Worktrees sort oldest-first by checkout creation time (which tracks
		// PR order in practice), with the workspace number as tiebreaker.
		type wsWithTime struct {
			ws        wsInfo
			createdAt time.Time
		}
		byAge := make([]wsWithTime, len(others))
		for i, ws := range others {
			byAge[i] = wsWithTime{ws: ws}
			if ws.Worktree != nil && ws.Worktree.CheckoutPath != "" {
				byAge[i].createdAt = worktreeCreatedAt(ws.Worktree.CheckoutPath)
			}
		}
		sort.SliceStable(byAge, func(a, b int) bool {
			if !byAge[a].createdAt.Equal(byAge[b].createdAt) {
				return byAge[a].createdAt.Before(byAge[b].createdAt)
			}
			return byAge[a].ws.Number < byAge[b].ws.Number
		})
		for i, w := range byAge {
			others[i] = w.ws
		}
		for _, ws := range others {
			crumb := name + " › " + ws.Label
			wt := &node{kind: "worktree", label: ws.Label, path: crumb, wsID: ws.ID, expanded: true}
			if ws.Worktree != nil {
				wt.branch = gitBranch(ws.Worktree.CheckoutPath)
				wt.ticket = ticketFrom(wt.branch, ws.Label)
				wt.ghSlug = slugFor(ws.Worktree.RepoRoot)
				if wt.branch != "" && wt.branch != ws.Label {
					wt.path = crumb + "  (" + wt.branch + ")"
				}
			}
			wt.children = paneNodes(ws.ID, crumb)
			repo.children = append(repo.children, wt)
		}
		roots = append(roots, repo)
	}
	for _, r := range roots {
		aggregateStatus(r)
	}
	return roots
}

// flatten returns every node in tree order plus parallel slices of lowercased
// labels and branches, fed to the fuzzy matcher in one shot per keystroke. A
// node's branch entry is "" when it adds nothing over the label (no branch, or
// identical), so the matcher skips it.
func flatten(roots []*node) ([]*node, []string, []string) {
	var nodes []*node
	var labels []string
	var branches []string
	var walk func(n *node)
	walk = func(n *node) {
		nodes = append(nodes, n)
		label := strings.ToLower(n.label)
		labels = append(labels, label)
		branch := strings.ToLower(n.branch)
		if branch == label {
			branch = ""
		}
		branches = append(branches, branch)
		for _, c := range n.children {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return nodes, labels, branches
}

// ---- key bindings (bubbles/key) ----

type keyMap struct {
	Up     key.Binding
	Down   key.Binding
	Select key.Binding
	Toggle key.Binding
	Cancel key.Binding
	Filter key.Binding
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Filter, k.Up, k.Down, k.Select, k.Toggle, k.Cancel}
}
func (k keyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }

func defaultKeys() keyMap {
	return keyMap{
		Up:     key.NewBinding(key.WithKeys("up", "ctrl+p"), key.WithHelp("↑/^p", "up")),
		Down:   key.NewBinding(key.WithKeys("down", "ctrl+n"), key.WithHelp("↓/^n", "down")),
		Select: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		Toggle: key.NewBinding(key.WithKeys("ctrl+t"), key.WithHelp("^t", "panes")),
		Cancel: key.NewBinding(key.WithKeys("esc", "ctrl+c"), key.WithHelp("esc", "cancel")),
		Filter: key.NewBinding(key.WithKeys(), key.WithHelp("type", "search")),
	}
}

// ---- bubbletea model ----

type rowItem struct {
	n     *node
	depth int
	match bool
	score int   // fuzzy score (only meaningful when match)
	idx   []int // matched character positions, for highlighting
}

type model struct {
	roots         []*node
	allNodes      []*node  // flattened, parallel to lowerLabels/lowerBranches/lowerMetas
	lowerLabels   []string // lowercased labels for the fuzzy matcher
	lowerBranches []string // lowercased branches ("" when same as label); extra match text
	lowerMetas    []string // ticket + PR number per node ("" when none); extra match text
	slugNodes     map[string][]*node // nodes with a branch, grouped by GitHub slug
	prPending     int                // in-flight gh fetches; cache is saved when it reaches 0
	cache         prCache            // loaded at startup, merged as repoPRsMsg arrive
	initCmds      []tea.Cmd          // PR fetches to fan out from Init
	rows          []rowItem
	cursor        int
	showPanes     bool // panes are hidden by default; ctrl+t toggles them
	ti            textinput.Model
	vp            viewport.Model
	help          help.Model
	keys          keyMap
	action        []string // herdr CLI args to run after quit (nil = no action)
}

var (
	stPrompt = lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Bold(true)
	stSel    = lipgloss.NewStyle().Background(lipgloss.Color("8")).Bold(true)
	stMatch  = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	stCrumb  = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	// stDev colors the "(dev)" marker shown in the prompt for non-release builds.
	stDev = lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Bold(true)

	// Ticket / PR prefix: ticket in the crumb teal, PR number colored by state.
	stTicket   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))   // teal
	stPROpen   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))  // green
	stPRDraft  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))   // dim
	stPRMerged = lipgloss.NewStyle().Foreground(lipgloss.Color("135")) // purple
	stPRClosed = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))   // red

	// Gutter status dots, mirroring herdr's sidebar state_dot.
	stDotBlocked = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))  // red
	stDotWorking = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // yellow
	stDotDone    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))  // teal
	stDotIdle    = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // green
	stDotNone    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))  // dim
)

// statusDot renders the 1-rune left-gutter indicator for an aggregated agent
// status, mirroring herdr's sidebar state_dot (blocked/working/done filled,
// idle hollow, none/unknown dim).
func statusDot(s string) string {
	switch s {
	case "blocked":
		return stDotBlocked.Render("●")
	case "working":
		return stDotWorking.Render("●")
	case "done":
		return stDotDone.Render("●")
	case "idle":
		return stDotIdle.Render("○")
	default: // "unknown", "" (shell / no agent)
		return stDotNone.Render("·")
	}
}

func prStyle(state string) lipgloss.Style {
	switch state {
	case "draft":
		return stPRDraft
	case "merged":
		return stPRMerged
	case "closed":
		return stPRClosed
	default: // "open"
		return stPROpen
	}
}

// layoutPrefixes stamps per-sibling-group column widths for the ticket / PR
// prefix so sibling rows align. Widths consider all siblings (not just
// visible rows) to keep alignment stable while filtering. Nodes with neither
// ticket nor PR keep 0 = no prefix at all. Re-run whenever PR annotations
// change.
func layoutPrefixes(roots []*node) {
	var layout func(siblings []*node)
	layout = func(siblings []*node) {
		tktW, prW := 0, 0
		for _, n := range siblings {
			if len(n.ticket) > tktW {
				tktW = len(n.ticket)
			}
			if n.pr != nil {
				if w := len(fmt.Sprintf("#%d", n.pr.Number)); w > prW {
					prW = w
				}
			}
		}
		for _, n := range siblings {
			if n.ticket != "" || n.pr != nil {
				n.tktW, n.prW = tktW, prW
			} else {
				n.tktW, n.prW = 0, 0
			}
			layout(n.children)
		}
	}
	layout(roots)
}

// prPrefixPlain builds the "TICKET #123  " prefix without styling, for the
// selected row (nested ANSI inside the selection background misrenders).
// Empty when the node has neither ticket nor PR; either column pads with
// spaces when a sibling has it and this row doesn't.
func prPrefixPlain(n *node) string {
	if n.tktW == 0 && n.prW == 0 {
		return ""
	}
	s := ""
	if n.tktW > 0 {
		s = fmt.Sprintf("%-*s", n.tktW, n.ticket)
	}
	if n.prW > 0 {
		pr := ""
		if n.pr != nil {
			pr = fmt.Sprintf("#%d", n.pr.Number)
		}
		if s != "" {
			s += " "
		}
		s += fmt.Sprintf("%-*s", n.prW, pr)
	}
	return s + "  "
}

// prPrefix is the styled variant used on unselected rows.
func prPrefix(n *node) string {
	if n.tktW == 0 && n.prW == 0 {
		return ""
	}
	s := ""
	if n.tktW > 0 {
		s = stTicket.Render(fmt.Sprintf("%-*s", n.tktW, n.ticket))
	}
	if n.prW > 0 {
		if s != "" {
			s += " "
		}
		if n.pr != nil {
			pr := fmt.Sprintf("#%d", n.pr.Number)
			s += prStyle(n.pr.State).Render(pr) + strings.Repeat(" ", n.prW-len(pr))
		} else {
			s += strings.Repeat(" ", n.prW)
		}
	}
	return s + "  "
}

// promptText builds the textinput prompt. Release builds (version stamped from a
// vX.Y.Z tag) show "goto ❯ "; non-release builds (`dev` / `local-<sha>`) insert
// an orange "(dev)" marker so it's obvious you're not on a published version.
func promptText() string {
	if strings.HasPrefix(version, "v") {
		return stPrompt.Render("goto ❯ ")
	}
	return stPrompt.Render("goto (") + stDev.Render("dev") + stPrompt.Render(") ❯ ")
}

// refreshMetas rebuilds the ticket / PR-number search corpus, parallel to
// allNodes. This is what lets a query like "1234" find the row showing
// "#1234", or a ticket that only came from a PR title. Rebuilt whenever PR
// annotations change (they arrive async from gh).
func (m *model) refreshMetas() {
	m.lowerMetas = m.lowerMetas[:0]
	for _, n := range m.allNodes {
		meta := strings.ToLower(n.ticket)
		if n.pr != nil {
			if meta != "" {
				meta += " "
			}
			meta += fmt.Sprintf("#%d", n.pr.Number)
		}
		m.lowerMetas = append(m.lowerMetas, meta)
	}
}

// kindBonus biases the ranking so repo/worktree names outrank panes on ties,
// keeping the "type h -> herdr" feel even though fuzzy does the real scoring.
func kindBonus(kind string) int {
	switch kind {
	case "repo":
		return 8
	case "worktree":
		return 4
	default:
		return 0
	}
}

func (m *model) applyFilter() {
	q := strings.ToLower(m.ti.Value())
	filtering := q != ""

	type hit struct {
		score int
		idx   []int
	}
	hits := map[*node]hit{}
	if filtering {
		for _, mt := range fuzzy.Find(q, m.lowerLabels) {
			hits[m.allNodes[mt.Index]] = hit{mt.Score, mt.MatchedIndexes}
		}
		// Branches and ticket/PR metadata are matched separately so queries
		// like "feat/x" find a worktree whose label is the "feat-x" folder
		// slug, and "1234" finds the row showing PR #1234. These hits carry
		// no MatchedIndexes: those indexes point into the branch/meta text,
		// not the rendered label, so there is nothing to highlight.
		for _, corpus := range [][]string{m.lowerBranches, m.lowerMetas} {
			for _, mt := range fuzzy.Find(q, corpus) {
				n := m.allNodes[mt.Index]
				if h, ok := hits[n]; !ok || mt.Score > h.score {
					hits[n] = hit{mt.Score, nil}
				}
			}
		}
	}

	visible := func(n *node) bool { return m.showPanes || n.kind != "pane" }

	var subtree func(n *node) bool
	subtree = func(n *node) bool {
		if !visible(n) {
			return false
		}
		if !filtering {
			return true
		}
		if _, ok := hits[n]; ok {
			return true
		}
		for _, c := range n.children {
			if subtree(c) {
				return true
			}
		}
		return false
	}

	m.rows = m.rows[:0]
	var walk func(n *node, depth int)
	walk = func(n *node, depth int) {
		h, ok := hits[n]
		m.rows = append(m.rows, rowItem{n: n, depth: depth, match: ok, score: h.score, idx: h.idx})
		if n.expanded || filtering {
			for _, c := range n.children {
				if subtree(c) {
					walk(c, depth+1)
				}
			}
		}
	}
	for _, r := range m.roots {
		if subtree(r) {
			walk(r, 0)
		}
	}

	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *model) parentOf(target *node) *node {
	for _, n := range m.allNodes {
		for _, c := range n.children {
			if c == target {
				return n
			}
		}
	}
	return nil
}

// keepCursorOn tries to keep the cursor on the same node after rows change; if
// that node is gone (e.g. a pane we just hid), it falls back to its parent.
func (m *model) keepCursorOn(target *node) {
	if target == nil {
		return
	}
	candidates := []*node{target}
	if p := m.parentOf(target); p != nil {
		candidates = append(candidates, p)
	}
	for _, want := range candidates {
		for i, r := range m.rows {
			if r.n == want {
				m.cursor = i
				return
			}
		}
	}
}

func (m *model) selectBestMatch() {
	best := -1
	var bestScore int
	for i, r := range m.rows {
		if !r.match {
			continue
		}
		sc := r.score + kindBonus(r.n.kind)
		if best == -1 || sc > bestScore {
			best, bestScore = i, sc
		}
	}
	if best >= 0 {
		m.cursor = best
	} else {
		m.cursor = 0
	}
}

func rowLine(r rowItem, selected bool) string {
	indent := strings.Repeat("  ", r.depth)
	// The status dot sits in its own gutter to the left, outside the selection
	// highlight (nested ANSI on a background renders inconsistently across
	// terminals).
	dot := statusDot(r.n.status) + " "
	if selected {
		// Plain text inside the highlight. Constant 2-col gutter keeps content
		// aligned whether or not the row is selected.
		return dot + stSel.Render("▌ "+indent+plain(r))
	}
	name := r.n.label
	if r.match {
		name = highlight(r.n.label, r.idx)
	}
	return dot + "  " + indent + prPrefix(r.n) + name
}

// highlight styles the fuzzy-matched characters within a label.
func highlight(label string, idx []int) string {
	if len(idx) == 0 {
		return label
	}
	set := make(map[int]bool, len(idx))
	for _, i := range idx {
		set[i] = true
	}
	var b strings.Builder
	for i, r := range []rune(label) {
		if set[i] {
			b.WriteString(stMatch.Render(string(r)))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// plain renders a row's content without styling; the number gutter and indent
// are prepended by rowLine.
func plain(r rowItem) string {
	return prPrefixPlain(r.n) + r.n.label
}

func (m *model) renderContent() {
	var b strings.Builder
	for i, r := range m.rows {
		b.WriteString(rowLine(r, i == m.cursor))
		if i < len(m.rows)-1 {
			b.WriteString("\n")
		}
	}
	m.vp.SetContent(b.String())
	m.ensureVisible()
}

func (m *model) ensureVisible() {
	h := m.vp.Height
	if h <= 0 {
		return
	}
	if m.cursor < m.vp.YOffset {
		m.vp.SetYOffset(m.cursor)
	} else if m.cursor >= m.vp.YOffset+h {
		m.vp.SetYOffset(m.cursor - h + 1)
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(append([]tea.Cmd{textinput.Blink}, m.initCmds...)...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.vp.Width = msg.Width
		m.vp.Height = msg.Height - 3 // prompt + breadcrumb + help
		if m.vp.Height < 1 {
			m.vp.Height = 1
		}
		m.help.Width = msg.Width
		m.renderContent()
		return m, nil

	case repoPRsMsg:
		if !msg.err {
			annotatePRs(m.slugNodes[msg.slug], msg.byBranch)
			m.cache.Repos[msg.slug] = repoPRs{FetchedAt: time.Now(), Branches: msg.byBranch}
			layoutPrefixes(m.roots)
			// PR numbers are searchable, so a fresh annotation can change the
			// results of the query being typed; re-filter, keeping the cursor
			// on the same node.
			m.refreshMetas()
			var cur *node
			if m.cursor >= 0 && m.cursor < len(m.rows) {
				cur = m.rows[m.cursor].n
			}
			m.applyFilter()
			m.keepCursorOn(cur)
			m.renderContent()
		}
		m.prPending--
		if m.prPending <= 0 {
			return m, savePRCacheCmd(m.cache)
		}
		return m, nil

	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Cancel):
			return m, tea.Quit
		case key.Matches(msg, m.keys.Select):
			if m.cursor >= 0 && m.cursor < len(m.rows) {
				n := m.rows[m.cursor].n
				if n.kind == "pane" {
					m.action = []string{"agent", "focus", n.paneID}
				} else {
					m.action = []string{"workspace", "focus", n.wsID}
				}
			}
			return m, tea.Quit
		case key.Matches(msg, m.keys.Up):
			if m.cursor > 0 {
				m.cursor--
				m.renderContent()
			}
			return m, nil
		case key.Matches(msg, m.keys.Down):
			if m.cursor < len(m.rows)-1 {
				m.cursor++
				m.renderContent()
			}
			return m, nil
		case key.Matches(msg, m.keys.Toggle):
			var cur *node
			if m.cursor >= 0 && m.cursor < len(m.rows) {
				cur = m.rows[m.cursor].n
			}
			m.showPanes = !m.showPanes
			m.applyFilter()
			m.keepCursorOn(cur)
			m.renderContent()
			return m, saveStateCmd(persisted{ShowPanes: m.showPanes})
		}

		var cmd tea.Cmd
		m.ti, cmd = m.ti.Update(msg)
		m.applyFilter()
		if m.ti.Value() != "" {
			m.selectBestMatch()
		}
		m.renderContent()
		return m, cmd

	default:
		var cmd tea.Cmd
		m.ti, cmd = m.ti.Update(msg)
		return m, cmd
	}
}

func (m model) View() string {
	crumb := ""
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		crumb = stCrumb.Render(" " + m.rows[m.cursor].n.path)
	}
	return m.ti.View() + "\n" + crumb + "\n" + m.vp.View() + "\n" + m.help.View(m.keys)
}

// version is the release tag; overridden at build time via
// -ldflags "-X main.version=vX.Y.Z" (see scripts/release.sh and CI).
var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Println(version)
		return
	}
	dump := len(os.Args) > 1 && os.Args[1] == "-dump"

	var ws wsResp
	var pn paneResp
	if err := loadJSON([]string{"workspace", "list"}, &ws); err != nil {
		fmt.Fprintln(os.Stderr, "workspace list:", err)
		os.Exit(1)
	}
	if err := loadJSON([]string{"pane", "list"}, &pn); err != nil {
		fmt.Fprintln(os.Stderr, "pane list:", err)
		os.Exit(1)
	}
	roots := buildTree(ws.Result.Workspaces, pn.Result.Panes)
	allNodes, lowerLabels, lowerBranches := flatten(roots)

	// Annotate PR info from the disk cache (instant), then schedule a gh
	// refresh per repo whose cache entry is missing or no longer fresh. The
	// fetches run from Init, after the TUI is already on screen.
	slugNodes := map[string][]*node{}
	for _, n := range allNodes {
		if n.ghSlug != "" && n.branch != "" {
			slugNodes[n.ghSlug] = append(slugNodes[n.ghSlug], n)
		}
	}
	cache := loadPRCache()
	for slug, nodes := range slugNodes {
		if entry, ok := cache.Repos[slug]; ok {
			annotatePRs(nodes, entry.Branches)
		}
	}
	layoutPrefixes(roots)
	var initCmds []tea.Cmd
	for slug, nodes := range slugNodes {
		if entry, ok := cache.Repos[slug]; ok && time.Since(entry.FetchedAt) < prCacheFresh {
			continue
		}
		// One --head lookup per unique local branch; main/master checkouts are
		// skipped (a PR with that head would be someone else's release train).
		seen := map[string]bool{}
		var branches []string
		for _, n := range nodes {
			if n.branch == "main" || n.branch == "master" || seen[n.branch] {
				continue
			}
			seen[n.branch] = true
			branches = append(branches, n.branch)
		}
		if len(branches) == 0 {
			continue
		}
		initCmds = append(initCmds, fetchRepoPRsCmd(slug, branches, cache.Repos[slug].Branches))
	}

	if dump {
		var walk func(n *node, d int)
		walk = func(n *node, d int) {
			prefix := ""
			if n.ticket != "" {
				prefix += n.ticket + " "
			}
			if n.pr != nil {
				prefix += fmt.Sprintf("#%d(%s) ", n.pr.Number, n.pr.State)
			}
			id := n.wsID
			if n.kind == "pane" {
				id = n.paneID
			}
			branch := ""
			if n.branch != "" {
				branch = " " + n.branch
			}
			fmt.Printf("%s%s%s\t(%s %s %s%s)\n", strings.Repeat("  ", d), prefix, n.label, n.kind, n.status, id, branch)
			for _, c := range n.children {
				walk(c, d+1)
			}
		}
		for _, r := range roots {
			walk(r, 0)
		}
		return
	}

	ti := textinput.New()
	ti.Prompt = promptText()
	ti.PromptStyle = lipgloss.NewStyle() // colors are already baked into the prompt
	ti.Focus()

	m := model{
		roots:         roots,
		allNodes:      allNodes,
		lowerLabels:   lowerLabels,
		lowerBranches: lowerBranches,
		slugNodes:     slugNodes,
		prPending:     len(initCmds),
		cache:         cache,
		initCmds:      initCmds,
		showPanes:     loadState().ShowPanes,
		ti:            ti,
		vp:            viewport.New(80, 20),
		help:          help.New(),
		keys:          defaultKeys(),
	}
	m.refreshMetas()
	m.applyFilter()
	m.renderContent()

	res, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if final := res.(model); final.action != nil {
		exec.Command(herdrBin(), final.action...).Run()
	}
}
