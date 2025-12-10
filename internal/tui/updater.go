package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"mcpm/internal/builder"
)

type updateState int

const (
	updateStateBuilding updateState = iota
	updateStateConfigEnv
	updateStateSelectingClient
	updateStateDone
)

type UpdateModel struct {
	state       updateState
	err         error
	serverPath  string
	serverName  string
	buildResult *builder.BuildResult
	global      bool

	spinner    spinner.Model
	inputs     []textinput.Model
	focusIndex int

	clients  []string
	selected map[int]bool
	cursor   int
}

func NewUpdateModel(serverPath, serverName string, global bool) UpdateModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = focusedStyle

	scope := "Current Dir"
	if global {
		scope = "Global"
	}

	return UpdateModel{
		state:      updateStateBuilding,
		serverPath: serverPath,
		serverName: serverName,
		global:     global,
		spinner:    s,
		clients:    []string{fmt.Sprintf("Claude Code (%s)", scope), fmt.Sprintf("Gemini CLI (%s)", scope)},
		selected:   map[int]bool{0: true, 1: true},
	}
}

func (m UpdateModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, buildRepoCmd(m.serverPath))
}

func (m UpdateModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" || msg.String() == "esc" {
			return m, tea.Quit
		}
		if m.state == updateStateConfigEnv {
			return m.updateEnvInputs(msg)
		}
		if m.state == updateStateSelectingClient {
			return m.updateClientSelection(msg)
		}

	case msgBuilt:
		m.buildResult = msg.result
		if len(m.buildResult.EnvNeeds) > 0 {
			m.state = updateStateConfigEnv
			m.inputs = make([]textinput.Model, len(m.buildResult.EnvNeeds))
			for i, envName := range m.buildResult.EnvNeeds {
				t := textinput.New()
				t.Placeholder = envName
				t.Prompt = fmt.Sprintf("%s: ", envName)
				if i == 0 {
					t.Focus()
				}
				m.inputs[i] = t
			}
			return m, nil
		}
		m.state = updateStateSelectingClient
		return m, nil

	case msgError:
		m.err = msg.err
		return m, tea.Quit

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m UpdateModel) View() string {
	if m.err != nil {
		return errorStyle.Render(fmt.Sprintf("Error: %v\n", m.err))
	}

	switch m.state {
	case updateStateBuilding:
		return fmt.Sprintf("%s Rebuilding %s...", m.spinner.View(), m.serverName)
	case updateStateConfigEnv:
		var b strings.Builder
		b.WriteString(titleStyle.Render("Configuration Required"))
		b.WriteString("\n\n")
		for i := range m.inputs {
			b.WriteString(m.inputs[i].View())
			b.WriteString("\n")
		}
		b.WriteString("\n(Press Enter to confirm)")
		return b.String()
	case updateStateSelectingClient:
		var b strings.Builder
		b.WriteString(titleStyle.Render("Re-register with Clients?"))
		b.WriteString("\n")
		for i, choice := range m.clients {
			cursor := " "
			if m.cursor == i {
				cursor = ">"
			}
			checked := "[ ]"
			if m.selected[i] {
				checked = "[x]"
			}
			line := fmt.Sprintf("%s %s %s", cursor, checked, choice)
			if m.cursor == i {
				b.WriteString(focusedStyle.Render(line))
			} else {
				b.WriteString(line)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n(Space to toggle, Enter to update)")
		return b.String()
	case updateStateDone:
		return successStyle.Render("Successfully updated and configured!")
	}
	return ""
}

func (m UpdateModel) updateEnvInputs(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		if m.focusIndex == len(m.inputs)-1 {
			m.state = updateStateSelectingClient
			return m, nil
		}
		m.inputs[m.focusIndex].Blur()
		m.focusIndex++
		m.inputs[m.focusIndex].Focus()
		return m, nil

	case "tab":
		m.inputs[m.focusIndex].Blur()
		m.focusIndex = (m.focusIndex + 1) % len(m.inputs)
		m.inputs[m.focusIndex].Focus()
		return m, nil
	}

	var cmd tea.Cmd
	m.inputs[m.focusIndex], cmd = m.inputs[m.focusIndex].Update(msg)
	return m, cmd
}

func (m UpdateModel) updateClientSelection(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.clients)-1 {
			m.cursor++
		}
	case " ":
		m.selected[m.cursor] = !m.selected[m.cursor]
	case "enter":
		m.state = updateStateDone

		// Gather Env
		finalEnv := make(map[string]string)
		if m.buildResult != nil {
			for i, need := range m.buildResult.EnvNeeds {
				if i < len(m.inputs) {
					finalEnv[need] = m.inputs[i].Value()
				}
			}
		}

		// Only register if at least one client is selected
		if m.selected[0] || m.selected[1] {
			// Import injector
			err := registerClients(m.buildResult, m.selected, finalEnv, m.global)
			if err != nil {
				m.err = err
				return m, tea.Quit
			}
		}
		return m, tea.Quit
	}
	return m, nil
}
