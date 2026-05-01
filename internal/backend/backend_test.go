package backend_test

import (
	"context"
	"testing"

	"github.com/danielfbm/tkn-act/internal/backend"
)

type fake struct {
	prepared bool
	tasks    []backend.TaskInvocation
	cleaned  bool
}

func (f *fake) Prepare(_ context.Context, _ backend.RunSpec) error { f.prepared = true; return nil }
func (f *fake) RunTask(_ context.Context, t backend.TaskInvocation) (backend.TaskResult, error) {
	f.tasks = append(f.tasks, t)
	return backend.TaskResult{Status: backend.TaskSucceeded}, nil
}
func (f *fake) Cleanup(_ context.Context) error { f.cleaned = true; return nil }

func TestFakeImplementsBackend(t *testing.T) {
	var _ backend.Backend = (*fake)(nil) // compile-time assertion
}
