package tui

import "github.com/charmbracelet/bubbles/key"

// keyMap holds the key bindings (FR-8, Decisions §2). `h` is deliberately
// unbound; `l` is reserved for lock/unlock.
type keyMap struct {
	Up, Down, PageUp, PageDown, Home, End key.Binding
	Collapse, Expand, Toggle              key.Binding
	Enter, Refresh, Quit                  key.Binding
	Detach, Checkout, Lock, Prune         key.Binding
	New, Remove                           key.Binding
	// Actions are the keys implemented by later tasks (T10–T11); they are
	// routed to Model.action.
	Actions key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		PageUp:   key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up")),
		PageDown: key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "page down")),
		Home:     key.NewBinding(key.WithKeys("home"), key.WithHelp("home", "top")),
		End:      key.NewBinding(key.WithKeys("end"), key.WithHelp("end", "bottom")),
		Collapse: key.NewBinding(key.WithKeys("left"), key.WithHelp("←", "collapse/parent")),
		Expand:   key.NewBinding(key.WithKeys("right"), key.WithHelp("→", "expand")),
		Toggle:   key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "toggle")),
		Enter:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "details")),
		Refresh:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Quit:     key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Detach:   key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "detach")),
		Checkout: key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "checkout")),
		Lock:     key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "lock/unlock")),
		Prune:    key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "prune")),
		New:      key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new worktree")),
		Remove:   key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "remove worktree")),
		Actions:  key.NewBinding(key.WithKeys("o", "/", "?")),
	}
}
