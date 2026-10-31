package sessions

// ApplyLive returns a copy of ss with Live/Busy recomputed from procs, plus the
// set of live worktree paths (as given to b's constructor). Rules (FR-6):
//   - A Claude session is live (busy/idle) when a proc carries its SessionID.
//   - A Codex proc marks the most recently active Codex session bound to the
//     worktree of its cwd (best-effort; Codex has no busy/idle).
//   - A worktree is live if a live session binds to it (by transcript cwd), or,
//     for procs with no matching session, if the proc's launch cwd binds to it.
//
// ss is not modified. b may be nil, in which case no worktrees are reported.
func ApplyLive(ss []Session, procs []LiveProc, b *Binder) ([]Session, map[string]bool) {
	out := make([]Session, len(ss))
	copy(out, ss)
	liveWT := map[string]bool{}
	bind := func(cwd string) (string, bool) {
		if b == nil {
			return "", false
		}
		return b.Bind(cwd)
	}

	byID := make(map[string]int, len(out))
	for i := range out {
		out[i].Live, out[i].Busy = false, false
		if out[i].Agent == AgentClaude && out[i].ID != "" {
			byID[out[i].ID] = i
		}
	}

	var orphans []LiveProc
	for _, p := range procs {
		if p.Agent == AgentClaude && p.SessionID != "" {
			if i, ok := byID[p.SessionID]; ok {
				out[i].Live = true
				out[i].Busy = out[i].Busy || p.Busy
				continue
			}
		}
		orphans = append(orphans, p)
	}

	for _, p := range orphans {
		wt, ok := bind(p.Cwd)
		if !ok {
			continue
		}
		if p.Agent == AgentCodex {
			best := -1
			for i := range out {
				if out[i].Agent != AgentCodex || out[i].Live {
					continue
				}
				if w, ok := bind(out[i].Cwd); !ok || w != wt {
					continue
				}
				if best < 0 || out[i].LastActivity.After(out[best].LastActivity) {
					best = i
				}
			}
			if best >= 0 {
				out[best].Live = true
				continue // worktree marked below via the session's own binding
			}
		}
		liveWT[wt] = true
	}

	for i := range out {
		if out[i].Live {
			if wt, ok := bind(out[i].Cwd); ok {
				liveWT[wt] = true
			}
		}
	}
	return out, liveWT
}
