package sessions

import (
	"testing"
	"time"
)

func TestApplyLive(t *testing.T) {
	root := t.TempDir()
	repo, wt := root+"/x/repo", root+"/x/repo-wt"
	other := root + "/x/other"
	b := NewBinder([]string{repo, wt, other})
	now := time.Now()
	ss := []Session{
		{Agent: AgentClaude, ID: "S", Cwd: repo + "/sub"},
		{Agent: AgentClaude, ID: "T", Cwd: repo, Live: true, Busy: true}, // stale flags reset
		{Agent: AgentCodex, ID: "c-old", Cwd: wt, LastActivity: now.Add(-time.Hour)},
		{Agent: AgentCodex, ID: "c-new", Cwd: wt, LastActivity: now},
	}
	procs := []LiveProc{
		{PID: 10, Agent: AgentClaude, SessionID: "S", Busy: true, Cwd: other}, // launch cwd ignored for binding
		{PID: 11, Agent: AgentCodex, Cwd: wt},
	}
	out, live := ApplyLive(ss, procs, b)
	if !out[0].Live || !out[0].Busy {
		t.Errorf("S: %+v", out[0])
	}
	if out[1].Live || out[1].Busy {
		t.Errorf("T should be reset: %+v", out[1])
	}
	if out[2].Live || !out[3].Live || out[3].Busy {
		t.Errorf("codex: old=%+v new=%+v", out[2], out[3])
	}
	if !live[repo] || !live[wt] || live[other] {
		t.Errorf("live worktrees = %v", live)
	}
	if ss[0].Live || !ss[1].Live {
		t.Error("input modified")
	}
}

func TestApplyLiveOrphanProc(t *testing.T) {
	root := t.TempDir()
	wt := root + "/x/repo-wt"
	b := NewBinder([]string{root + "/x/repo", wt})
	// FR-6 AC: fake lister {pid 1, codex, cwd /x/repo-wt} -> worktree live.
	_, live := ApplyLive(nil, []LiveProc{{PID: 1, Agent: AgentCodex, Cwd: wt}}, b)
	if !live[wt] || len(live) != 1 {
		t.Errorf("live = %v", live)
	}
	// Claude registry entry whose session isn't loaded binds via launch cwd.
	_, live = ApplyLive(nil, []LiveProc{{PID: 2, Agent: AgentClaude, SessionID: "nope", Cwd: wt + "/a"}}, b)
	if !live[wt] {
		t.Errorf("live = %v", live)
	}
	_, live = ApplyLive(nil, []LiveProc{{PID: 3, Agent: AgentClaude, Cwd: "/elsewhere"}}, b)
	if len(live) != 0 {
		t.Errorf("live = %v", live)
	}
}
