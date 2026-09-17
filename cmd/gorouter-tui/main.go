package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/gorouter/gorouter/internal/daemon"
)

var version = "dev"

type statusMsg struct { value string; err error }
type model struct { ipcPath string; status string; err error }

func fetchStatus(path string) tea.Cmd { return func() tea.Msg { response, err := daemon.CallIPC(context.Background(), path, daemon.IPCRequest{ID:"tui-status", Method:"status"}); if err != nil { return statusMsg{err:err} }; data, err := json.MarshalIndent(response.Result, "", "  "); return statusMsg{value:string(data), err:err} } }

func (m model) Init() tea.Cmd { return fetchStatus(m.ipcPath) }
func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case statusMsg: m.status, m.err = msg.value, msg.err; return m, nil
	case tea.KeyPressMsg:
		switch msg.String() { case "q", "ctrl+c": return m, tea.Quit; case "r": return m, fetchStatus(m.ipcPath) }
	}
	return m, nil
}
func (m model) View() tea.View {
	content := fmt.Sprintf("GoRouter TUI (%s)\n\nIPC: %s\n\n%s\n\n[r] reload  [q] quit\n", version, m.ipcPath, m.status)
	if m.err != nil { content += "\nerror: " + m.err.Error() + "\n" }
	return tea.NewView(content)
}

func main() {
	defaults, err := daemon.DefaultConfig(); if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
	program := tea.NewProgram(model{ipcPath:defaults.IPCPath, status:"loading..."})
	if _, err := program.Run(); err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
}
