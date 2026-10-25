package tui

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"mcpm/internal/builder"
)

func updateEnvModel(n int) UpdateModel {
	m := NewUpdateModel("/tmp/server", "demo", false)
	m.state = updateStateConfigEnv
	m.inputs = make([]textinput.Model, n)
	for i := range m.inputs {
		m.inputs[i] = textinput.New()
	}
	m.inputs[0].Focus()
	return m
}

func TestNewUpdateModelLocal(t *testing.T) {
	m := NewUpdateModel("/tmp/server", "demo", false)
	if m.state != updateStateBuilding {
		t.Fatalf("expected updateStateBuilding, got %v", m.state)
	}
	if m.serverPath != "/tmp/server" || m.serverName != "demo" || m.global {
		t.Fatalf("unexpected fields: path=%q name=%q global=%v", m.serverPath, m.serverName, m.global)
	}
	want := []string{"Claude Code (Current Dir)", "Gemini CLI (Current Dir)"}
	if len(m.clients) != len(want) {
		t.Fatalf("expected %d clients, got %v", len(want), m.clients)
	}
	for i := range want {
		if m.clients[i] != want[i] {
			t.Fatalf("client %d: expected %q, got %q", i, want[i], m.clients[i])
		}
		if !m.selected[i] {
			t.Fatalf("expected client %d to be selected by default", i)
		}
	}
}

func TestNewUpdateModelGlobal(t *testing.T) {
	m := NewUpdateModel("", "", true)
	if !m.global {
		t.Fatal("expected global to be set")
	}
	if m.clients[0] != "Claude Code (Global)" || m.clients[1] != "Gemini CLI (Global)" {
		t.Fatalf("unexpected client labels: %v", m.clients)
	}
}

func TestUpdateModelInitReturnsCmd(t *testing.T) {
	if NewUpdateModel("", "", false).Init() == nil {
		t.Fatal("expected a non-nil init command")
	}
}

func TestUpdateModelQuitKeys(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyCtrlC}, {Type: tea.KeyEsc}} {
		for _, state := range []updateState{updateStateBuilding, updateStateConfigEnv, updateStateSelectingClient} {
			m := updateEnvModel(1)
			m.state = state
			_, cmd := m.Update(key)
			if !isQuit(cmd) {
				t.Fatalf("key %q in state %v: expected tea.Quit", key.String(), state)
			}
		}
	}
}

func TestUpdateModelKeyIgnoredWhileBuilding(t *testing.T) {
	m := NewUpdateModel("", "", false)
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("expected nil cmd, got %v", cmd)
	}
	if s := got.(UpdateModel).state; s != updateStateBuilding {
		t.Fatalf("expected updateStateBuilding, got %v", s)
	}
}

func TestUpdateModelBuiltWithEnvNeeds(t *testing.T) {
	m := NewUpdateModel("", "", false)
	res := &builder.BuildResult{EnvNeeds: []string{"API_KEY", "TOKEN"}}
	got, cmd := m.Update(msgBuilt{result: res})
	m = got.(UpdateModel)
	if cmd != nil {
		t.Fatalf("expected nil cmd, got %v", cmd)
	}
	if m.state != updateStateConfigEnv {
		t.Fatalf("expected updateStateConfigEnv, got %v", m.state)
	}
	if m.buildResult != res {
		t.Fatal("expected build result to be stored")
	}
	if len(m.inputs) != 2 {
		t.Fatalf("expected 2 inputs, got %d", len(m.inputs))
	}
	for i, name := range res.EnvNeeds {
		if m.inputs[i].Placeholder != name || m.inputs[i].Prompt != name+": " {
			t.Fatalf("input %d: placeholder=%q prompt=%q", i, m.inputs[i].Placeholder, m.inputs[i].Prompt)
		}
	}
	if !m.inputs[0].Focused() || m.inputs[1].Focused() {
		t.Fatal("expected only the first input to be focused")
	}
}

func TestUpdateModelBuiltWithoutEnvNeeds(t *testing.T) {
	got, cmd := NewUpdateModel("", "", false).Update(msgBuilt{result: &builder.BuildResult{}})
	if cmd != nil {
		t.Fatalf("expected nil cmd, got %v", cmd)
	}
	if s := got.(UpdateModel).state; s != updateStateSelectingClient {
		t.Fatalf("expected updateStateSelectingClient, got %v", s)
	}
}

func TestUpdateModelErrorMsg(t *testing.T) {
	want := errors.New("boom")
	got, cmd := NewUpdateModel("", "", false).Update(msgError{err: want})
	m := got.(UpdateModel)
	if !errors.Is(m.Err(), want) {
		t.Fatalf("expected Err() to return %v, got %v", want, m.Err())
	}
	if !isQuit(cmd) {
		t.Fatal("expected tea.Quit")
	}
	if v := m.View(); !strings.Contains(v, "Error: boom") {
		t.Fatalf("expected error in view, got %q", v)
	}
}

func TestUpdateModelErrNilByDefault(t *testing.T) {
	if err := NewUpdateModel("", "", false).Err(); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestUpdateModelSpinnerTick(t *testing.T) {
	m := NewUpdateModel("", "", false)
	got, _ := m.Update(spinner.TickMsg{})
	if s := got.(UpdateModel).state; s != updateStateBuilding {
		t.Fatalf("expected state unchanged, got %v", s)
	}
}

func TestUpdateModelUnknownMsg(t *testing.T) {
	got, cmd := NewUpdateModel("", "", false).Update(struct{}{})
	if cmd != nil {
		t.Fatalf("expected nil cmd, got %v", cmd)
	}
	if s := got.(UpdateModel).state; s != updateStateBuilding {
		t.Fatalf("expected state unchanged, got %v", s)
	}
}

func TestUpdateModelViewBuilding(t *testing.T) {
	v := NewUpdateModel("", "demo", false).View()
	if !strings.Contains(v, "Rebuilding demo...") {
		t.Fatalf("unexpected view: %q", v)
	}
}

func TestUpdateModelViewConfigEnv(t *testing.T) {
	got, _ := NewUpdateModel("", "", false).Update(msgBuilt{result: &builder.BuildResult{EnvNeeds: []string{"API_KEY"}}})
	v := got.(UpdateModel).View()
	for _, want := range []string{"Configuration Required", "API_KEY: ", "(Press Enter to confirm)"} {
		if !strings.Contains(v, want) {
			t.Fatalf("expected %q in view, got %q", want, v)
		}
	}
}

func TestUpdateModelViewSelectingClient(t *testing.T) {
	m := NewUpdateModel("", "", false)
	m.state = updateStateSelectingClient
	m.selected[1] = false
	v := m.View()
	for _, want := range []string{
		"Re-register with Clients?",
		"> [x] Claude Code (Current Dir)",
		"  [ ] Gemini CLI (Current Dir)",
		"(Space to toggle, Enter to update)",
	} {
		if !strings.Contains(v, want) {
			t.Fatalf("expected %q in view, got %q", want, v)
		}
	}
}

func TestUpdateModelViewDone(t *testing.T) {
	m := NewUpdateModel("", "", false)
	m.state = updateStateDone
	if v := m.View(); !strings.Contains(v, "Successfully updated and configured!") {
		t.Fatalf("unexpected view: %q", v)
	}
}

func TestUpdateModelViewUnknownState(t *testing.T) {
	m := NewUpdateModel("", "", false)
	m.state = updateState(99)
	if v := m.View(); v != "" {
		t.Fatalf("expected empty view, got %q", v)
	}
}

func TestUpdateModelEnvEnterAdvancesFocus(t *testing.T) {
	got, cmd := updateEnvModel(2).Update(tea.KeyMsg{Type: tea.KeyEnter})
	m := got.(UpdateModel)
	if cmd != nil {
		t.Fatalf("expected nil cmd, got %v", cmd)
	}
	if m.focusIndex != 1 || m.inputs[0].Focused() || !m.inputs[1].Focused() {
		t.Fatalf("expected focus to move to input 1, got index %d", m.focusIndex)
	}
	if m.state != updateStateConfigEnv {
		t.Fatalf("expected updateStateConfigEnv, got %v", m.state)
	}
}

func TestUpdateModelEnvEnterOnLastInput(t *testing.T) {
	got, cmd := updateEnvModel(1).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("expected nil cmd, got %v", cmd)
	}
	if s := got.(UpdateModel).state; s != updateStateSelectingClient {
		t.Fatalf("expected updateStateSelectingClient, got %v", s)
	}
}

func TestUpdateModelEnvTabWraps(t *testing.T) {
	m := updateEnvModel(2)
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = got.(UpdateModel)
	if m.focusIndex != 1 {
		t.Fatalf("expected focus index 1, got %d", m.focusIndex)
	}
	got, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = got.(UpdateModel)
	if m.focusIndex != 0 || !m.inputs[0].Focused() || m.inputs[1].Focused() {
		t.Fatalf("expected focus to wrap to input 0, got index %d", m.focusIndex)
	}
}

func TestUpdateModelEnvTyping(t *testing.T) {
	got, _ := updateEnvModel(1).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("abc")})
	if v := got.(UpdateModel).inputs[0].Value(); v != "abc" {
		t.Fatalf("expected input value %q, got %q", "abc", v)
	}
}

func TestUpdateModelClientCursorMovement(t *testing.T) {
	m := NewUpdateModel("", "", false)
	m.state = updateStateSelectingClient
	steps := []struct {
		key  tea.KeyMsg
		want int
	}{
		{tea.KeyMsg{Type: tea.KeyUp}, 0},
		{tea.KeyMsg{Type: tea.KeyDown}, 1},
		{tea.KeyMsg{Type: tea.KeyDown}, 1},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")}, 0},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}, 1},
	}
	for i, s := range steps {
		got, cmd := m.Update(s.key)
		if cmd != nil {
			t.Fatalf("step %d: expected nil cmd, got %v", i, cmd)
		}
		m = got.(UpdateModel)
		if m.cursor != s.want {
			t.Fatalf("step %d (%q): expected cursor %d, got %d", i, s.key.String(), s.want, m.cursor)
		}
	}
}

func TestUpdateModelClientToggle(t *testing.T) {
	m := NewUpdateModel("", "", false)
	m.state = updateStateSelectingClient
	space := tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}
	got, _ := m.Update(space)
	if got.(UpdateModel).selected[0] {
		t.Fatal("expected client 0 to be deselected")
	}
	got, _ = got.(UpdateModel).Update(space)
	if !got.(UpdateModel).selected[0] {
		t.Fatal("expected client 0 to be reselected")
	}
}

func TestUpdateModelClientEnterNoClients(t *testing.T) {
	m := NewUpdateModel("", "", false)
	m.state = updateStateSelectingClient
	m.selected = map[int]bool{}
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = got.(UpdateModel)
	if m.state != updateStateDone {
		t.Fatalf("expected updateStateDone, got %v", m.state)
	}
	if m.Err() != nil {
		t.Fatalf("expected no error, got %v", m.Err())
	}
	if !isQuit(cmd) {
		t.Fatal("expected tea.Quit")
	}
}

func TestUpdateModelClientEnterRegistersGemini(t *testing.T) {
	dir := chdirTemp(t)
	m := updateEnvModel(1)
	m.state = updateStateSelectingClient
	m.inputs[0].SetValue("secret")
	m.buildResult = &builder.BuildResult{
		Command:  "node",
		Args:     []string{filepath.Join(dir, ".mcp", "servers", "demo", "index.js")},
		EnvNeeds: []string{"API_KEY"},
	}
	m.selected = map[int]bool{1: true}

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = got.(UpdateModel)
	if m.Err() != nil {
		t.Fatalf("expected no error, got %v", m.Err())
	}
	if m.state != updateStateDone {
		t.Fatalf("expected updateStateDone, got %v", m.state)
	}
	if !isQuit(cmd) {
		t.Fatal("expected tea.Quit")
	}

	data, err := os.ReadFile(filepath.Join(dir, ".gemini", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		McpServers map[string]struct {
			Env map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if v := cfg.McpServers["demo"].Env["API_KEY"]; v != "secret" {
		t.Fatalf("expected API_KEY=secret in settings, got %q (%s)", v, data)
	}
}

func TestUpdateModelClientEnterRegisterError(t *testing.T) {
	chdirTemp(t)
	m := NewUpdateModel("", "", false)
	m.state = updateStateSelectingClient
	m.buildResult = &builder.BuildResult{Command: "node", Args: []string{"index.js"}}
	m.selected = map[int]bool{1: true}

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got.(UpdateModel).Err() == nil {
		t.Fatal("expected a registration error")
	}
	if !isQuit(cmd) {
		t.Fatal("expected tea.Quit")
	}
}

func TestUpdateModelClientIgnoresOtherKeys(t *testing.T) {
	m := NewUpdateModel("", "", false)
	m.state = updateStateSelectingClient
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")})
	if cmd != nil {
		t.Fatalf("expected nil cmd, got %v", cmd)
	}
	if s := got.(UpdateModel).state; s != updateStateSelectingClient {
		t.Fatalf("expected state unchanged, got %v", s)
	}
}
