package tui

import (
	"encoding/json"
	"fmt"
	"strconv"

	tea "charm.land/bubbletea/v2"
	"github.com/fm39hz/gobroom/internal/termtext"
)

type usageDetailRequest struct {
	key   string
	epoch uint64
	pane  int
}

func newUsageDetailForm(id int64) *formState {
	form := buildForm("Usage detail · request only authorized classes", "usage.detail", []fieldSpec{
		{key: "includeExtensions", label: "Public extensions · true/false", placeholder: "true or false"},
		{key: "includePrivate", label: "Private extensions · host grant required · true/false", placeholder: "true or false"},
		{key: "includeRaw", label: "Raw usage fragment · host grant required · true/false", placeholder: "true or false"},
	}, map[string]string{"includeExtensions": "true", "includePrivate": "false", "includeRaw": "false"})
	form.extra["id"] = id
	return form
}

func usageDetailParams(form *formState) (map[string]any, error) {
	id, ok := form.extra["id"].(int64)
	if !ok || id <= 0 {
		return nil, fmt.Errorf("usage event ID must be positive")
	}
	options := make(map[string]bool, 3)
	for _, key := range []string{"includeExtensions", "includePrivate", "includeRaw"} {
		value, found := usageDetailFormValue(form, key)
		if !found {
			return nil, fmt.Errorf("usage detail option %q is missing", key)
		}
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("usage detail option %q must be true or false", key)
		}
		options[key] = parsed
	}
	if options["includePrivate"] {
		options["includeExtensions"] = true
	}
	return map[string]any{
		"id":                id,
		"includeExtensions": options["includeExtensions"],
		"includePrivate":    options["includePrivate"],
		"includeRaw":        options["includeRaw"],
	}, nil
}

func usageDetailFormValue(form *formState, key string) (string, bool) {
	for index, field := range form.fields {
		if field.key == key && index < len(form.inputs) {
			return form.inputs[index].Value(), true
		}
	}
	return "", false
}

func formatUsageDetail(raw json.RawMessage) string {
	return termtext.JSON(raw)
}

func (m *app) requestUsageDetail(params map[string]any) tea.Cmd {
	m.usageDetailEpoch++
	request := usageDetailRequest{
		key:   strconv.FormatInt(params["id"].(int64), 10),
		epoch: m.usageDetailEpoch,
		pane:  m.activeContextIndex(),
	}
	command := m.invoke("usage.detail", params)
	return func() tea.Msg {
		message := command().(actionMsg)
		message.usageDetail = request
		return message
	}
}

func (m *app) acceptsUsageDetail(request usageDetailRequest) bool {
	if request.epoch == 0 || request.epoch != m.usageDetailEpoch || request.key == "" {
		return false
	}
	if m.mode != modeBrowse || m.active().definition.view != "usage" || m.activeContextIndex() != request.pane {
		return false
	}
	selected := m.selectedEntry()
	if selected == nil || selected.key != request.key {
		return false
	}
	value, ok := selected.payload.(usageRecord)
	return ok && value.ID > 0 && strconv.FormatInt(value.ID, 10) == request.key
}

func (m *app) applyUsageDetail(message actionMsg) {
	m.usageDetailEpoch++
	m.loading = false
	m.mode = modeBrowse
	m.form = nil
	m.lastError = ""
	if message.err != nil {
		m.status = "usage detail denied or unavailable · check host disclosure policy"
		m.previewExtra, m.previewTitle, m.previewKey = "", "", ""
		m.usageDetailPreview = nil
	} else {
		m.status = "usage detail loaded"
		m.previewExtra = formatUsageDetail(message.result)
		m.previewTitle = "Usage detail"
		m.previewKey = message.usageDetail.key
		request := message.usageDetail
		m.usageDetailPreview = &request
	}
	m.syncDetail()
	if message.err == nil {
		m.detail.GotoTop()
	}
}

func (m *app) showsAcceptedUsageDetail(selected entry) bool {
	request := m.usageDetailPreview
	if request == nil || m.active().definition.view != "usage" || m.activeContextIndex() != request.pane {
		return false
	}
	if selected.key != request.key || m.previewKey != request.key {
		return false
	}
	value, ok := selected.payload.(usageRecord)
	return ok && value.ID > 0 && strconv.FormatInt(value.ID, 10) == request.key
}
