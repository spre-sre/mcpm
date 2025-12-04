package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"mcpm/internal/builder"
)

type sessionState int

const (
	stateFetching sessionState = iota
	stateBuilding
	stateConfigEnv
	stateSelectingClient
	stateDone
)

type Model struct {
	state       sessionState
	err         error
	repoUrl     string
	repoName    string // User input name
	repoPath    string
	buildResult *builder.BuildResult
	global      bool

	spinner    spinner.Model
	inputs     []textinput.Model
	focusIndex int

	clients  []string
	selected map[int]bool
	cursor   int
}

func NewInstallModel(repoUrl, repoName string, global bool) Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = focusedStyle

	scope := "Current Dir"
	if global {
		scope = "Global"
	}

	return Model{
		state:    stateFetching,
		repoUrl:  repoUrl,
		repoName: repoName,
		global:   global,
		spinner:  s,
		clients:  []string{fmt.Sprintf("Claude Code (%s)", scope), fmt.Sprintf("Gemini CLI (%s)", scope)},
		selected: map[int]bool{0: true, 1: true},
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, fetchRepoCmd(m.repoUrl))
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" || msg.String() == "esc" {
			return m, tea.Quit
		}
		if m.state == stateConfigEnv {
			return updateEnvInputs(m, msg)
		}
		if m.state == stateSelectingClient {
			return updateClientSelection(m, msg)
		}

	case msgRepoFetched:
		m.repoPath = msg.path
		m.state = stateBuilding
		return m, buildRepoCmd(m.repoPath)

	case msgBuilt:
		m.buildResult = msg.result
		if len(m.buildResult.EnvNeeds) > 0 {
			m.state = stateConfigEnv
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
		m.state = stateSelectingClient
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

func (m Model) View() string {
	if m.err != nil {
		return errorStyle.Render(fmt.Sprintf("L Error: %v\n", m.err))
	}

	switch m.state {
	case stateFetching:
		return fmt.Sprintf("%s Fetching %s...", m.spinner.View(), m.repoName)
	case stateBuilding:
		return fmt.Sprintf("%s Analyzing and building project...", m.spinner.View())
	case stateConfigEnv:
		var b strings.Builder
		b.WriteString(titleStyle.Render("Configuration Required"))
		b.WriteString("\n\n")
		for i := range m.inputs {
			b.WriteString(m.inputs[i].View())
			b.WriteString("\n")
		}
		b.WriteString("\n(Press Enter to confirm)")
		return b.String()
	case stateSelectingClient:
		var b strings.Builder
		b.WriteString(titleStyle.Render("Select Target Clients"))
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
		b.WriteString("\n(Space to toggle, Enter to install)")
		return b.String()
	case stateDone:
		return successStyle.Render("( Successfully installed and configured!")
	}
	return ""
}
