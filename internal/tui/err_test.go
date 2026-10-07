package tui

import (
	"errors"
	"testing"
)

func TestModelErrNil(t *testing.T) {
	m := NewInstallModel("https://example.com/repo.git", "repo", false)
	if m.Err() != nil {
		t.Fatalf("expected nil, got %v", m.Err())
	}
}

func TestModelErrAfterMsgError(t *testing.T) {
	m := NewInstallModel("https://example.com/repo.git", "repo", false)
	e := errors.New("clone failed")
	updated, _ := m.Update(msgError{err: e})
	got := updated.(Model).Err()
	if got != e {
		t.Fatalf("expected %v, got %v", e, got)
	}
}

func TestUpdateModelErrNil(t *testing.T) {
	m := NewUpdateModel("/tmp/server", "server", false)
	if m.Err() != nil {
		t.Fatalf("expected nil, got %v", m.Err())
	}
}

func TestUpdateModelErrAfterMsgError(t *testing.T) {
	m := NewUpdateModel("/tmp/server", "server", false)
	e := errors.New("build failed")
	updated, _ := m.Update(msgError{err: e})
	got := updated.(UpdateModel).Err()
	if got != e {
		t.Fatalf("expected %v, got %v", e, got)
	}
}
