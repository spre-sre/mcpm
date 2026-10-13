package tui

import "github.com/charmbracelet/lipgloss"

var (
	focusedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("205")) // Pink
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99")).MarginBottom(1)
	successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))  // Green
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("196")) // Red
)
