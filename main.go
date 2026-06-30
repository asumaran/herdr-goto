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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

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
	path     string // breadcrumb, e.g. "monorepo-front › infra-metrics"
	wsID     string // workspace to focus (repo/worktree, and a pane's workspace)
	paneID   string // pane to focus
	num      int    // 1..9 for repo headers, else 0
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
				wsID: wsID, paneID: p.ID, expanded: true,
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
	for i, g := range glist {
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
		num := 0
		if i < 9 {
			num = i + 1
		}
		repo := &node{kind: "repo", label: name, path: name, wsID: main.ID, num: num, expanded: true}
		repo.children = append(repo.children, paneNodes(main.ID, name)...)

		others := []wsInfo{}
		for _, ws := range g.wss {
			if ws.ID != main.ID {
				others = append(others, ws)
			}
		}
		sort.SliceStable(others, func(a, b int) bool { return others[a].Number < others[b].Number })
		for _, ws := range others {
			crumb := name + " › " + ws.Label
			wt := &node{kind: "worktree", label: ws.Label, path: crumb, wsID: ws.ID, expanded: true}
			wt.children = paneNodes(ws.ID, crumb)
			repo.children = append(repo.children, wt)
		}
		roots = append(roots, repo)
	}
	return roots
}

// flatten returns every node in tree order plus a parallel slice of lowercased
// labels, fed to the fuzzy matcher in one shot per keystroke.
func flatten(roots []*node) ([]*node, []string) {
	var nodes []*node
	var labels []string
	var walk func(n *node)
	walk = func(n *node) {
		nodes = append(nodes, n)
		labels = append(labels, strings.ToLower(n.label))
		for _, c := range n.children {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return nodes, labels
}

// ---- key bindings (bubbles/key) ----

type keyMap struct {
	Jump   key.Binding
	Up     key.Binding
	Down   key.Binding
	Select key.Binding
	Toggle key.Binding
	Cancel key.Binding
	Filter key.Binding
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Jump, k.Filter, k.Up, k.Down, k.Select, k.Toggle, k.Cancel}
}
func (k keyMap) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }

func defaultKeys() keyMap {
	return keyMap{
		Jump:   key.NewBinding(key.WithKeys("1", "2", "3", "4", "5", "6", "7", "8", "9"), key.WithHelp("1-9", "jump repo")),
		Up:     key.NewBinding(key.WithKeys("up", "ctrl+p"), key.WithHelp("↑", "up")),
		Down:   key.NewBinding(key.WithKeys("down", "ctrl+n"), key.WithHelp("↓", "down")),
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
	num   int // jump number shown as [n]; original when unfiltered, else 1..N
	match bool
	score int   // fuzzy score (only meaningful when match)
	idx   []int // matched character positions, for highlighting
}

type model struct {
	roots       []*node
	allNodes    []*node  // flattened, parallel to lowerLabels
	lowerLabels []string // lowercased labels for the fuzzy matcher
	rows        []rowItem
	cursor      int
	showPanes   bool // panes are hidden by default; ctrl+t toggles them
	ti          textinput.Model
	vp          viewport.Model
	help        help.Model
	keys        keyMap
	action      []string // herdr CLI args to run after quit (nil = no action)
}

var (
	stPrompt = lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Bold(true)
	stSel    = lipgloss.NewStyle().Background(lipgloss.Color("8")).Bold(true)
	stMatch  = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	stCrumb  = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	stNum    = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	// stDev colors the "(dev)" marker shown in the prompt for non-release builds.
	stDev = lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Bold(true)
)

// promptText builds the textinput prompt. Release builds (version stamped from a
// vX.Y.Z tag) show "goto ❯ "; non-release builds (`dev` / `local-<sha>`) insert
// an orange "(dev)" marker so it's obvious you're not on a published version.
func promptText() string {
	if strings.HasPrefix(version, "v") {
		return stPrompt.Render("goto ❯ ")
	}
	return stPrompt.Render("goto (") + stDev.Render("dev") + stPrompt.Render(") ❯ ")
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

	// Number the repo rows: original sidebar number when unfiltered, otherwise
	// 1..N over the visible results so the digit you see is the one you press.
	seq := 0
	for i := range m.rows {
		if m.rows[i].n.kind != "repo" {
			continue
		}
		if filtering {
			seq++
			if seq <= 9 {
				m.rows[i].num = seq
			} else {
				m.rows[i].num = 0
			}
		} else {
			m.rows[i].num = m.rows[i].n.num
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
	if selected {
		// Plain text inside the highlight (nested ANSI on a background renders
		// inconsistently across terminals). Constant 2-col gutter keeps content
		// aligned whether or not the row is selected.
		return stSel.Render("▌ " + indent + plain(r))
	}
	num := ""
	if r.n.kind == "repo" && r.num > 0 {
		num = stNum.Render(fmt.Sprintf("[%d] ", r.num))
	}
	name := r.n.label
	if r.match {
		name = highlight(r.n.label, r.idx)
	}
	return "  " + indent + num + name
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

func plain(r rowItem) string {
	if r.n.kind == "repo" && r.num > 0 {
		return fmt.Sprintf("[%d] %s", r.num, r.n.label)
	}
	return r.n.label
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

func (m model) Init() tea.Cmd { return textinput.Blink }

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

		// A digit always jumps to the numbered repo as currently shown (sidebar
		// number when unfiltered, 1..N over the results when filtering). Digits
		// are never search text.
		if len(msg.Runes) == 1 && msg.Runes[0] >= '1' && msg.Runes[0] <= '9' {
			want := int(msg.Runes[0] - '0')
			for _, r := range m.rows {
				if r.n.kind == "repo" && r.num == want {
					m.action = []string{"workspace", "focus", r.n.wsID}
					return m, tea.Quit
				}
			}
			return m, nil
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

	if dump {
		var walk func(n *node, d int)
		walk = func(n *node, d int) {
			prefix := ""
			if n.kind == "repo" && n.num > 0 {
				prefix = fmt.Sprintf("[%d] ", n.num)
			}
			id := n.wsID
			if n.kind == "pane" {
				id = n.paneID
			}
			fmt.Printf("%s%s%s\t(%s %s)\n", strings.Repeat("  ", d), prefix, n.label, n.kind, id)
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

	allNodes, lowerLabels := flatten(roots)
	m := model{
		roots:       roots,
		allNodes:    allNodes,
		lowerLabels: lowerLabels,
		showPanes:   loadState().ShowPanes,
		ti:          ti,
		vp:          viewport.New(80, 20),
		help:        help.New(),
		keys:        defaultKeys(),
	}
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
