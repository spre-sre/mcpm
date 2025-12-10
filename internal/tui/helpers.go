package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"mcpm/internal/builder"
	"mcpm/internal/injector"
)

func updateEnvInputs(m Model, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		if m.focusIndex == len(m.inputs)-1 {
			m.state = stateSelectingClient
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

func updateClientSelection(m Model, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
		m.state = stateDone

		// Gather Env
		finalEnv := make(map[string]string)
		if m.buildResult != nil {
			for i, need := range m.buildResult.EnvNeeds {
				finalEnv[need] = m.inputs[i].Value()
			}
		}

		// Map selection
		var tools []injector.TargetTool
		if m.selected[0] {
			tools = append(tools, injector.TargetClaudeCode)
		}
		if m.selected[1] {
			tools = append(tools, injector.TargetGeminiCLI)
		}

		err := injector.Register(m.buildResult, tools, finalEnv, m.global)
		if err != nil {
			m.err = err
			return m, tea.Quit
		}
		return m, tea.Quit
	}
	return m, nil
}

// registerClients is a helper to register with selected clients
func registerClients(result *builder.BuildResult, selected map[int]bool, env map[string]string, global bool) error {
	var tools []injector.TargetTool
	if selected[0] {
		tools = append(tools, injector.TargetClaudeCode)
	}
	if selected[1] {
		tools = append(tools, injector.TargetGeminiCLI)
	}
	return injector.Register(result, tools, env, global)
}
