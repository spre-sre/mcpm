package cmd

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

type fakeModel struct {
	err error
}

func (m fakeModel) Init() tea.Cmd                           { return nil }
func (m fakeModel) Update(tea.Msg) (tea.Model, tea.Cmd)     { return m, nil }
func (m fakeModel) View() string                            { return "" }
func (m fakeModel) Err() error                              { return m.err }

type fakeModelNoErr struct{}

func (m fakeModelNoErr) Init() tea.Cmd                       { return nil }
func (m fakeModelNoErr) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (m fakeModelNoErr) View() string                        { return "" }

func TestTuiErrorRunErr(t *testing.T) {
	runErr := errors.New("program crashed")
	got := tuiError(fakeModel{}, runErr)
	if got != runErr {
		t.Fatalf("expected %v, got %v", runErr, got)
	}
}

func TestTuiErrorModelErr(t *testing.T) {
	modelErr := errors.New("clone failed")
	got := tuiError(fakeModel{err: modelErr}, nil)
	if got != modelErr {
		t.Fatalf("expected %v, got %v", modelErr, got)
	}
}

func TestTuiErrorBothNil(t *testing.T) {
	got := tuiError(fakeModel{}, nil)
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestTuiErrorNoErrMethod(t *testing.T) {
	got := tuiError(fakeModelNoErr{}, nil)
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}
