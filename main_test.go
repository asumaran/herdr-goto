package main

import (
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
)

func TestTicketFrom(t *testing.T) {
	cases := []struct {
		branch, label, want string
	}{
		{"feat-FED-2030-stargate-oci-pipeline", "", "FED-2030"},
		{"FED-2035-stargate-stg-oci-validation", "", "FED-2035"},
		{"fed-2031", "", "FED-2031"},
		{"cronus/plat-1193-e2e-encryption-proof", "", "PLAT-1193"},
		{"", "fed-2031", "FED-2031"},                        // label fallback
		{"feat-observability-wiring", "some-label", ""},     // no ticket anywhere
		{"feat-digital-lhciFixClosedServerConnection", "", ""},
		{"e2e-tests", "", ""},         // digit inside the key: not a ticket
		{"v1-2-migration", "", ""},    // single letter before the dash: not a ticket
		{"main", "", ""},
		{"feat-FED-2030-FED-2031-x", "", "FED-2030"}, // first match wins
	}
	for _, c := range cases {
		if got := ticketFrom(c.branch, c.label); got != c.want {
			t.Errorf("ticketFrom(%q, %q) = %q, want %q", c.branch, c.label, got, c.want)
		}
	}
}

func TestGithubSlugFromURL(t *testing.T) {
	cases := []struct{ url, want string }{
		{"git@github.com:masmovil/monorepo-front.git", "masmovil/monorepo-front"},
		{"git@github.com:asumaran/herdr-goto", "asumaran/herdr-goto"},
		{"ssh://git@github.com/owner/repo.git", "owner/repo"},
		{"https://github.com/owner/repo.git", "owner/repo"},
		{"https://github.com/owner/repo", "owner/repo"},
		{"https://github.com/owner/repo/", "owner/repo"},
		{"git@gitlab.com:owner/repo.git", ""}, // non-GitHub host
		{"https://github.com/owner", ""},      // no repo segment
		{"", ""},
	}
	for _, c := range cases {
		if got := githubSlugFromURL(c.url); got != c.want {
			t.Errorf("githubSlugFromURL(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

func TestOriginURL(t *testing.T) {
	config := `[core]
	repositoryformatversion = 0
[remote "upstream"]
	url = git@github.com:other/upstream.git
[remote "origin"]
	url = git@github.com:owner/repo.git
	fetch = +refs/heads/*:refs/remotes/origin/*
[branch "main"]
	remote = origin
`
	if got := originURL(config); got != "git@github.com:owner/repo.git" {
		t.Errorf("originURL = %q, want origin url", got)
	}
	if got := originURL("[core]\n\tbare = false\n"); got != "" {
		t.Errorf("originURL without origin = %q, want empty", got)
	}
}

func TestLayoutPrefixesAndPlainPrefix(t *testing.T) {
	repo := &node{kind: "repo", label: "monorepo-front", children: []*node{
		{kind: "worktree", label: "feat-stargate", ticket: "FED-2030", pr: &prRef{Number: 1274, State: "open"}},
		{kind: "worktree", label: "stg-validation", ticket: "FED-2035"},
		{kind: "worktree", label: "webvitals-faro", pr: &prRef{Number: 6449, State: "draft"}},
		{kind: "worktree", label: "observability-wiring"},
	}}
	layoutPrefixes([]*node{repo})

	want := []string{
		"FED-2030 #1274  ", // ticket + PR
		"FED-2035        ", // ticket, PR column padded
		"         #6449  ", // PR without ticket, ticket column padded
		"",                 // neither: no prefix at all
	}
	for i, c := range repo.children {
		if got := prPrefixPlain(c); got != want[i] {
			t.Errorf("child %d (%s): prefix %q, want %q", i, c.label, got, want[i])
		}
	}
	if got := prPrefixPlain(repo); got != "" {
		t.Errorf("repo without ticket/PR: prefix %q, want empty", got)
	}
}

// TestSearchByTicketAndPRNumber covers that digits are plain search text and
// that the ticket / PR-number corpus is matched: typing a PR number or a
// ticket key finds the row that displays it, even when neither appears in the
// label or branch.
func TestSearchByTicketAndPRNumber(t *testing.T) {
	byPR := &node{kind: "worktree", label: "webvitals-faro", pr: &prRef{Number: 6449, State: "open"}}
	byTicket := &node{kind: "worktree", label: "stg-validation", ticket: "FED-2035"}
	repo := &node{kind: "repo", label: "monorepo-front", expanded: true, children: []*node{byPR, byTicket}}
	roots := []*node{repo}

	m := model{roots: roots, ti: textinput.New()}
	m.allNodes, m.lowerLabels, m.lowerBranches = flatten(roots)
	m.refreshMetas()

	matched := func(query string) map[*node]bool {
		m.ti.SetValue(query)
		m.applyFilter()
		out := map[*node]bool{}
		for _, r := range m.rows {
			if r.match {
				out[r.n] = true
			}
		}
		return out
	}

	if got := matched("6449"); !got[byPR] || got[byTicket] {
		t.Errorf("query 6449: matched %v, want only the PR #6449 node", got)
	}
	if got := matched("2035"); !got[byTicket] {
		t.Errorf("query 2035: matched %v, want the FED-2035 node", got)
	}
}

func TestGhPRRef(t *testing.T) {
	cases := []struct {
		in   ghPR
		want prRef
	}{
		{ghPR{Number: 1, State: "OPEN"}, prRef{Number: 1, State: "open"}},
		{ghPR{Number: 2, State: "OPEN", IsDraft: true}, prRef{Number: 2, State: "draft"}},
		{ghPR{Number: 3, State: "MERGED"}, prRef{Number: 3, State: "merged"}},
		{ghPR{Number: 4, State: "CLOSED"}, prRef{Number: 4, State: "closed"}},
		{ghPR{Number: 5, State: "OPEN", Title: "FED-2040: wire observability"}, prRef{Number: 5, State: "open", Ticket: "FED-2040"}},
	}
	for _, c := range cases {
		if got := c.in.ref(); got != c.want {
			t.Errorf("ref(%+v) = %+v, want %+v", c.in, got, c.want)
		}
	}
}
