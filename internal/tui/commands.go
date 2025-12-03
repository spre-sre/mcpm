package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"mcpm/internal/builder"
	"mcpm/internal/fetcher"
)

type msgRepoFetched struct{ path string }
type msgBuilt struct{ result *builder.BuildResult }
type msgError struct{ err error }

func fetchRepoCmd(url string) tea.Cmd {
	return func() tea.Msg {
		path, err := fetcher.Clone(url)
		if err != nil {
			return msgError{err}
		}
		return msgRepoFetched{path}
	}
}

func buildRepoCmd(path string) tea.Cmd {
	return func() tea.Msg {
		res, err := builder.DetectAndBuild(path)
		if err != nil {
			return msgError{err}
		}
		return msgBuilt{res}
	}
}
