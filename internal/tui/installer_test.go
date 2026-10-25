package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"mcpm/internal/builder"
)

func TestNewInstallModelLocal(t *testing.T) {
	m := NewInstallModel("https://example.com/owner/repo.git", "repo", false)
	if m.state != stateFetching {
		t.Fatalf("expected stateFetching, got %v", m.state)
	}
	if m.repoUrl != "https://example.com/owner/repo.git" || m.repoName != "repo" || m.global {
		t.Fatalf("unexpected fields: url=%q name=%q global=%v", m.repoUrl, m.repoName, m.global)
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

func TestNewInstallModelGlobal(t *testing.T) {
	m := NewInstallModel("", "", true)
	if !m.global {
		t.Fatal("expected global to be set")
	}
	if m.clients[0] != "Claude Code (Global)" || m.clients[1] != "Gemini CLI (Global)" {
		t.Fatalf("unexpected client labels: %v", m.clients)
	}
}

func TestModelInitReturnsCmd(t *testing.T) {
	if NewInstallModel("", "", false).Init() == nil {
		t.Fatal("expected a non-nil init command")
	}
}

func TestModelUpdateQuitKeys(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyCtrlC}, {Type: tea.KeyEsc}} {
		for _, state := range []sessionState{stateFetching, stateConfigEnv, stateSelectingClient} {
			m := envModel(1)
			m.state = state
			_, cmd := m.Update(key)
			if !isQuit(cmd) {
				t.Fatalf("key %q in state %v: expected tea.Quit", key.String(), state)
			}
		}
	}
}

func TestModelUpdateKeyIgnoredWhileFetching(t *testing.T) {
	m := NewInstallModel("", "", false)
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("expected nil cmd, got %v", cmd)
	}
	if s := got.(Model).state; s != stateFetching {
		t.Fatalf("expected stateFetching, got %v", s)
	}
}

func TestModelUpdateKeyDelegatesToEnvInputs(t *testing.T) {
	got, _ := envModel(2).Update(tea.KeyMsg{Type: tea.KeyTab})
	if i := got.(Model).focusIndex; i != 1 {
		t.Fatalf("expected focus index 1, got %d", i)
	}
}

func TestModelUpdateKeyDelegatesToClientSelection(t *testing.T) {
	m := NewInstallModel("", "", false)
	m.state = stateSelectingClient
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if c := got.(Model).cursor; c != 1 {
		t.Fatalf("expected cursor 1, got %d", c)
	}
}

func TestModelUpdateRepoFetched(t *testing.T) {
	m := NewInstallModel("", "", false)
	got, cmd := m.Update(msgRepoFetched{path: "/tmp/repo"})
	m = got.(Model)
	if m.state != stateBuilding {
		t.Fatalf("expected stateBuilding, got %v", m.state)
	}
	if m.repoPath != "/tmp/repo" {
		t.Fatalf("expected repoPath /tmp/repo, got %q", m.repoPath)
	}
	if cmd == nil {
		t.Fatal("expected a build command")
	}
}

func TestModelUpdateBuiltWithEnvNeeds(t *testing.T) {
	m := NewInstallModel("", "", false)
	m.state = stateBuilding
	res := &builder.BuildResult{EnvNeeds: []string{"API_KEY", "TOKEN"}}
	got, cmd := m.Update(msgBuilt{result: res})
	m = got.(Model)
	if cmd != nil {
		t.Fatalf("expected nil cmd, got %v", cmd)
	}
	if m.state != stateConfigEnv {
		t.Fatalf("expected stateConfigEnv, got %v", m.state)
	}
	if m.buildResult != res {
		t.Fatal("expected buildResult to be stored")
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

func TestModelUpdateBuiltWithoutEnvNeeds(t *testing.T) {
	m := NewInstallModel("", "", false)
	m.state = stateBuilding
	got, cmd := m.Update(msgBuilt{result: &builder.BuildResult{}})
	m = got.(Model)
	if cmd != nil {
		t.Fatalf("expected nil cmd, got %v", cmd)
	}
	if m.state != stateSelectingClient {
		t.Fatalf("expected stateSelectingClient, got %v", m.state)
	}
	if len(m.inputs) != 0 {
		t.Fatalf("expected no inputs, got %d", len(m.inputs))
	}
}

func TestModelUpdateErrorQuits(t *testing.T) {
	_, cmd := NewInstallModel("", "", false).Update(msgError{err: errors.New("boom")})
	if !isQuit(cmd) {
		t.Fatal("expected tea.Quit")
	}
}

func TestModelUpdateSpinnerTick(t *testing.T) {
	m := NewInstallModel("", "", false)
	_, cmd := m.Update(m.spinner.Tick())
	if cmd == nil {
		t.Fatal("expected the spinner to schedule another tick")
	}
}

func TestModelUpdateUnknownMsg(t *testing.T) {
	m := NewInstallModel("", "", false)
	got, cmd := m.Update(struct{}{})
	if cmd != nil {
		t.Fatalf("expected nil cmd, got %v", cmd)
	}
	if s := got.(Model).state; s != stateFetching {
		t.Fatalf("expected state unchanged, got %v", s)
	}
}

func TestModelViewError(t *testing.T) {
	m := NewInstallModel("", "repo", false)
	m.err = errors.New("boom")
	if v := m.View(); !strings.Contains(v, "Error: boom") {
		t.Fatalf("expected error in view, got %q", v)
	}
}

func TestModelViewFetching(t *testing.T) {
	v := NewInstallModel("", "my-repo", false).View()
	if !strings.Contains(v, "Fetching my-repo...") {
		t.Fatalf("unexpected fetching view: %q", v)
	}
}

func TestModelViewBuilding(t *testing.T) {
	m := NewInstallModel("", "", false)
	m.state = stateBuilding
	if v := m.View(); !strings.Contains(v, "Analyzing and building project...") {
		t.Fatalf("unexpected building view: %q", v)
	}
}

func TestModelViewConfigEnv(t *testing.T) {
	m := NewInstallModel("", "", false)
	got, _ := m.Update(msgBuilt{result: &builder.BuildResult{EnvNeeds: []string{"API_KEY"}}})
	v := got.(Model).View()
	for _, want := range []string{"Configuration Required", "API_KEY: ", "(Press Enter to confirm)"} {
		if !strings.Contains(v, want) {
			t.Fatalf("expected %q in view, got %q", want, v)
		}
	}
}

func TestModelViewSelectingClient(t *testing.T) {
	m := NewInstallModel("", "", false)
	m.state = stateSelectingClient
	m.selected[1] = false
	v := m.View()
	for _, want := range []string{
		"Select Target Clients",
		"> [x] Claude Code (Current Dir)",
		"  [ ] Gemini CLI (Current Dir)",
		"(Space to toggle, Enter to install)",
	} {
		if !strings.Contains(v, want) {
			t.Fatalf("expected %q in view, got %q", want, v)
		}
	}
}

func TestModelViewDone(t *testing.T) {
	m := NewInstallModel("", "", false)
	m.state = stateDone
	if v := m.View(); !strings.Contains(v, "Successfully installed and configured!") {
		t.Fatalf("unexpected done view: %q", v)
	}
}

func TestModelViewUnknownState(t *testing.T) {
	m := NewInstallModel("", "", false)
	m.state = sessionState(99)
	if v := m.View(); v != "" {
		t.Fatalf("expected empty view, got %q", v)
	}
}
