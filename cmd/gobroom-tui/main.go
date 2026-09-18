package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/fm39hz/gobroom/internal/daemon"
)

var version = "dev"

type tab struct {
	key, name, method string
}

var tabs = []tab{
	{key: "1", name: "daemon", method: "status"},
	{key: "2", name: "providers", method: "providers.list"},
	{key: "3", name: "connections", method: "connections.list"},
	{key: "4", name: "models", method: "models.list"},
	{key: "5", name: "combos", method: "combos.list"},
	{key: "6", name: "public models", method: "public_models.list"},
	{key: "7", name: "health", method: "health.list"},
	{key: "8", name: "quota", method: "quota.list"},
}

type responseMsg struct {
	method string
	value  string
	err    error
}

type form struct {
	title  string
	method string
	fields []string
	values []string
	active int
}

type model struct {
	ipcPath string
	tab     int
	data    string
	status  string
	err     error
	form    *form
}

func call(path, method string, params map[string]any) tea.Cmd {
	return func() tea.Msg {
		response, err := daemon.CallIPC(context.Background(), path, daemon.IPCRequest{ID: "tui", Method: method, Params: params})
		if err != nil {
			return responseMsg{method: method, err: err}
		}
		data, err := json.MarshalIndent(response.Result, "", "  ")
		return responseMsg{method: method, value: string(data), err: err}
	}
}

func fetch(path string, index int) tea.Cmd {
	return func() tea.Msg {
		response, err := daemon.CallIPC(context.Background(), path, daemon.IPCRequest{ID: "tui", Method: tabs[index].method})
		if err != nil {
			return responseMsg{method: tabs[index].method, err: err}
		}
		data, err := json.MarshalIndent(response.Result, "", "  ")
		return responseMsg{method: tabs[index].method, value: string(data), err: err}
	}
}

func (m model) Init() tea.Cmd { return fetch(m.ipcPath, m.tab) }

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case responseMsg:
		if m.form != nil {
			m.form = nil
		}
		m.data, m.err = msg.value, msg.err
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
		} else {
			m.status = msg.method + " completed"
		}
		return m, nil
	case tea.KeyPressMsg:
		key := msg.String()
		if m.form != nil {
			return m.updateForm(msg)
		}
		switch key {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "r":
			m.status = "loading..."
			return m, fetch(m.ipcPath, m.tab)
		case "n":
			if next := newForm(m.tab); next != nil {
				m.form = next
			}
			return m, nil
		case "f":
			if m.tab == 1 {
				m.form = &form{title: "refresh provider models", method: "providers.refresh_models", fields: []string{"nodeID"}, values: make([]string, 1)}
			}
			return m, nil
		case "a":
			if m.tab == 3 {
				m.form = &form{title: "upsert custom model", method: "custom_models.upsert", fields: []string{"id", "providerNodeID", "kind", "externalID", "displayName"}, values: []string{"", "", "custom", "", ""}}
			} else if m.tab == 2 {
				m.form = &form{title: "upsert logical model", method: "logical_models.upsert", fields: []string{"name", "targetRef"}, values: make([]string, 2)}
			}
			return m, nil
		case "d":
			if next := deleteForm(m.tab); next != nil {
				m.form = next
			} else {
				m.status = "delete is not available for this view"
			}
			return m, nil
		}
		for index, item := range tabs {
			if key == item.key {
				m.tab = index
				m.status = "loading..."
				return m, fetch(m.ipcPath, index)
			}
		}
	}
	return m, nil
}

func (m model) updateForm(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	f := m.form
	key := message.String()
	switch key {
	case "esc":
		m.form = nil
		return m, nil
	case "tab", "right":
		f.active = (f.active + 1) % len(f.fields)
		return m, nil
	case "shift+tab", "left":
		f.active = (f.active + len(f.fields) - 1) % len(f.fields)
		return m, nil
	case "enter":
		params, err := formParams(f)
		if err != nil {
			m.status = err.Error()
			return m, nil
		}
		m.status = "saving..."
		return m, call(m.ipcPath, f.method, params)
	case "backspace":
		if len(f.values[f.active]) > 0 {
			f.values[f.active] = f.values[f.active][:len(f.values[f.active])-1]
		}
		return m, nil
	}
	text := message.Key().Text
	if text != "" && !strings.ContainsAny(text, "\r\n\t") {
		f.values[f.active] += text
	}
	return m, nil
}

func newForm(index int) *form {
	switch index {
	case 1:
		return &form{title: "create provider", method: "providers.create", fields: []string{"name", "prefix", "baseURL", "protocol"}, values: make([]string, 4)}
	case 2:
		return &form{title: "create connection", method: "connections.create", fields: []string{"nodeID", "name", "credentialType", "secret", "priority"}, values: make([]string, 5)}
	case 4:
		return &form{title: "upsert combo", method: "combos.upsert", fields: []string{"name", "strategy", "members(comma-separated)"}, values: []string{"", "fallback", ""}}
	case 5:
		return &form{title: "publish model", method: "public_models.upsert", fields: []string{"name", "targetRef", "ownedBy"}, values: []string{"", "", "gobroom"}}
	default:
		return nil
	}
}

func deleteForm(index int) *form {
	switch index {
	case 1:
		return &form{title: "delete provider", method: "providers.delete", fields: []string{"id"}, values: make([]string, 1)}
	case 2:
		return &form{title: "delete connection", method: "connections.delete", fields: []string{"id"}, values: make([]string, 1)}
	case 4:
		return &form{title: "delete combo", method: "combos.delete", fields: []string{"name"}, values: make([]string, 1)}
	case 5:
		return &form{title: "unpublish model", method: "public_models.delete", fields: []string{"name"}, values: make([]string, 1)}
	default:
		return nil
	}
}

func formParams(f *form) (map[string]any, error) {
	params := map[string]any{}
	for i, field := range f.fields {
		optional := f.method == "custom_models.upsert" && (field == "providerNodeID" || field == "kind" || field == "externalID")
		optional = optional || field == "ownedBy" || field == "strategy"
		if strings.TrimSpace(f.values[i]) == "" && !optional {
			return nil, fmt.Errorf("%s is required", field)
		}
	}
	switch f.method {
	case "providers.delete", "connections.delete":
		params["id"] = f.values[0]
	case "combos.delete", "public_models.delete":
		params["name"] = f.values[0]
	case "providers.refresh_models":
		params["nodeID"] = f.values[0]
	case "logical_models.upsert":
		params["name"], params["targetRef"] = f.values[0], f.values[1]
	case "custom_models.upsert":
		params["id"], params["providerNodeID"], params["kind"], params["externalID"], params["displayName"] = f.values[0], f.values[1], f.values[2], f.values[3], f.values[4]
	case "providers.create":
		params["name"], params["prefix"], params["baseUrl"], params["protocol"] = f.values[0], f.values[1], f.values[2], f.values[3]
	case "connections.create":
		priority := 100
		if f.values[4] != "" {
			value, err := strconv.Atoi(f.values[4])
			if err != nil {
				return nil, fmt.Errorf("priority must be an integer")
			}
			priority = value
		}
		params["providerNodeID"], params["name"], params["credentialType"], params["secret"], params["priority"] = f.values[0], f.values[1], f.values[2], f.values[3], priority
	case "combos.upsert":
		members := make([]string, 0)
		for _, member := range strings.Split(f.values[2], ",") {
			if value := strings.TrimSpace(member); value != "" {
				members = append(members, value)
			}
		}
		params["name"], params["strategy"], params["members"] = f.values[0], f.values[1], members
	case "public_models.upsert":
		params["name"], params["targetRef"], params["ownedBy"] = f.values[0], f.values[1], f.values[2]
	}
	return params, nil
}

func (m model) View() tea.View {
	var b strings.Builder
	fmt.Fprintf(&b, "GoBroom TUI %s\nIPC: %s\n\n", version, m.ipcPath)
	for index, item := range tabs {
		if index == m.tab {
			fmt.Fprintf(&b, "[%s] %s  ", item.key, item.name)
		} else {
			fmt.Fprintf(&b, " %s  %s   ", item.key, item.name)
		}
	}
	b.WriteString("\n\n")
	if m.form != nil {
		fmt.Fprintf(&b, "%s\n\n", m.form.title)
		for index, field := range m.form.fields {
			marker := " "
			if index == m.form.active {
				marker = ">"
			}
			fmt.Fprintf(&b, "%s %-22s %s\n", marker, field+":", m.form.values[index])
		}
		b.WriteString("\n[type]  [tab/←→ field]  [enter save]  [esc cancel]\n")
		return tea.NewView(b.String())
	}
	b.WriteString(m.data)
	if m.err != nil {
		fmt.Fprintf(&b, "\n\nerror: %v", m.err)
	}
	fmt.Fprintf(&b, "\n\n%s\n[1-8] view  [r] refresh  [n] new  [q] quit\n", m.status)
	return tea.NewView(b.String())
}

func main() {
	defaults, err := daemon.DefaultConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	program := tea.NewProgram(model{ipcPath: defaults.IPCPath, status: "loading..."})
	if _, err := program.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
