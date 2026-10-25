package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"mcpm/internal/builder"
)

// envModel returns a model in the env-config state with n inputs, the first focused.
func envModel(n int) Model {
	m := NewInstallModel("https://example.com/owner/repo.git", "", false)
	m.state = stateConfigEnv
	m.inputs = make([]textinput.Model, n)
	for i := range m.inputs {
		m.inputs[i] = textinput.New()
	}
	m.inputs[0].Focus()
	return m
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestUpdateEnvInputsEnterAdvancesFocus(t *testing.T) {
	got, cmd := updateEnvInputs(envModel(2), tea.KeyMsg{Type: tea.KeyEnter})
	m := got.(Model)
	if cmd != nil {
		t.Fatalf("expected nil cmd, got %v", cmd)
	}
	if m.focusIndex != 1 || m.inputs[0].Focused() || !m.inputs[1].Focused() {
		t.Fatalf("expected focus on input 1, got index %d", m.focusIndex)
	}
	if m.state != stateConfigEnv {
		t.Fatalf("expected stateConfigEnv, got %v", m.state)
	}
}

func TestUpdateEnvInputsEnterOnLastSelectsClient(t *testing.T) {
	m := envModel(2)
	m.focusIndex = 1
	got, cmd := updateEnvInputs(m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("expected nil cmd, got %v", cmd)
	}
	if s := got.(Model).state; s != stateSelectingClient {
		t.Fatalf("expected stateSelectingClient, got %v", s)
	}
}

func TestUpdateEnvInputsTabWraps(t *testing.T) {
	m := envModel(2)
	m.inputs[0].Blur()
	m.focusIndex = 1
	m.inputs[1].Focus()
	got, _ := updateEnvInputs(m, tea.KeyMsg{Type: tea.KeyTab})
	m = got.(Model)
	if m.focusIndex != 0 || !m.inputs[0].Focused() || m.inputs[1].Focused() {
		t.Fatalf("expected focus to wrap to input 0, got index %d", m.focusIndex)
	}
}

func TestUpdateEnvInputsTypingUpdatesValue(t *testing.T) {
	got, _ := updateEnvInputs(envModel(1), tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if v := got.(Model).inputs[0].Value(); v != "x" {
		t.Fatalf("expected value %q, got %q", "x", v)
	}
}

func TestUpdateClientSelectionCursor(t *testing.T) {
	m := NewInstallModel("", "", false)
	m.state = stateSelectingClient

	got, _ := updateClientSelection(m, tea.KeyMsg{Type: tea.KeyUp})
	if c := got.(Model).cursor; c != 0 {
		t.Fatalf("up at top: expected cursor 0, got %d", c)
	}
	got, _ = updateClientSelection(got.(Model), tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if c := got.(Model).cursor; c != 1 {
		t.Fatalf("j: expected cursor 1, got %d", c)
	}
	got, _ = updateClientSelection(got.(Model), tea.KeyMsg{Type: tea.KeyDown})
	if c := got.(Model).cursor; c != 1 {
		t.Fatalf("down at bottom: expected cursor 1, got %d", c)
	}
	got, _ = updateClientSelection(got.(Model), tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if c := got.(Model).cursor; c != 0 {
		t.Fatalf("k: expected cursor 0, got %d", c)
	}
}

func TestUpdateClientSelectionToggle(t *testing.T) {
	m := NewInstallModel("", "", false)
	got, cmd := updateClientSelection(m, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")})
	if cmd != nil {
		t.Fatalf("expected nil cmd, got %v", cmd)
	}
	if got.(Model).selected[0] {
		t.Fatal("expected client 0 to be deselected")
	}
	got, _ = updateClientSelection(got.(Model), tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")})
	if !got.(Model).selected[0] {
		t.Fatal("expected client 0 to be reselected")
	}
}

func TestUpdateClientSelectionEnterNoClients(t *testing.T) {
	m := NewInstallModel("", "", false)
	m.selected = map[int]bool{}
	got, cmd := updateClientSelection(m, tea.KeyMsg{Type: tea.KeyEnter})
	m = got.(Model)
	if m.state != stateDone {
		t.Fatalf("expected stateDone, got %v", m.state)
	}
	if m.err != nil {
		t.Fatalf("expected no error, got %v", m.err)
	}
	if !isQuit(cmd) {
		t.Fatal("expected tea.Quit")
	}
}

func TestUpdateClientSelectionEnterRegistersGemini(t *testing.T) {
	dir := chdirTemp(t)
	m := envModel(1)
	m.inputs[0].SetValue("secret")
	m.buildResult = &builder.BuildResult{
		Command:  "node",
		Args:     []string{filepath.Join(dir, ".mcp", "servers", "demo", "index.js")},
		EnvNeeds: []string{"API_KEY"},
	}
	m.selected = map[int]bool{1: true}

	got, cmd := updateClientSelection(m, tea.KeyMsg{Type: tea.KeyEnter})
	m = got.(Model)
	if m.err != nil {
		t.Fatalf("expected no error, got %v", m.err)
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

func TestUpdateClientSelectionEnterRegisterError(t *testing.T) {
	chdirTemp(t)
	m := NewInstallModel("", "", false)
	m.buildResult = &builder.BuildResult{Command: "node", Args: []string{"index.js"}}
	m.selected = map[int]bool{1: true}

	got, cmd := updateClientSelection(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got.(Model).err == nil {
		t.Fatal("expected a registration error")
	}
	if !isQuit(cmd) {
		t.Fatal("expected tea.Quit")
	}
}

func TestUpdateClientSelectionIgnoresOtherKeys(t *testing.T) {
	m := NewInstallModel("", "", false)
	got, cmd := updateClientSelection(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")})
	if cmd != nil {
		t.Fatalf("expected nil cmd, got %v", cmd)
	}
	if s := got.(Model).state; s != m.state {
		t.Fatalf("expected state unchanged, got %v", s)
	}
}

func TestRegisterClientsNoneSelected(t *testing.T) {
	if err := registerClients(&builder.BuildResult{}, map[int]bool{}, nil, false); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestRegisterClientsGemini(t *testing.T) {
	dir := chdirTemp(t)
	result := &builder.BuildResult{
		Command: "node",
		Args:    []string{filepath.Join(dir, ".mcp", "servers", "demo", "index.js")},
	}
	if err := registerClients(result, map[int]bool{1: true}, nil, false); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".gemini", "settings.json")); err != nil {
		t.Fatalf("expected gemini settings to be written: %v", err)
	}
}

func TestRegisterClientsError(t *testing.T) {
	chdirTemp(t)
	result := &builder.BuildResult{Command: "node", Args: []string{"index.js"}}
	if err := registerClients(result, map[int]bool{1: true}, nil, false); err == nil {
		t.Fatal("expected an error for an underivable server name")
	}
}
