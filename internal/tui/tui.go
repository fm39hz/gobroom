package tui

import tea "charm.land/bubbletea/v2"

var version = "dev"

// Run starts the optional interactive control client. The daemon remains the
// sole owner of configuration and runtime state; the TUI only speaks IPC.
func Run(ipcPath, appVersion string) error {
	if appVersion != "" {
		version = appVersion
	}
	_, err := tea.NewProgram(newApp(ipcPath)).Run()
	return err
}
