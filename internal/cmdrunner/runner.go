// Package cmdrunner wraps os/exec so unit tests can substitute a fake.
package cmdrunner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// Runner runs commands. Real implementation execs; tests use Fake.
type Runner interface {
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	Run(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error
}

type real struct{}

func New() Runner { return real{} }

func (real) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("%s %s: %w (stderr: %s)", name, strings.Join(args, " "), err, stderr.String())
	}
	return stdout.Bytes(), nil
}

func (real) Run(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// Fake is a test helper. Use NewFake().Runner() to get a Runner that returns
// pre-canned responses keyed by full command line.
type Fake struct {
	mu     sync.Mutex
	canned map[string]cannedResponse
	calls  []string
}

type cannedResponse struct {
	out []byte
	err error
}

func NewFake() *Fake { return &Fake{canned: map[string]cannedResponse{}} }

func (f *Fake) Set(cmdline string, out []byte, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canned[cmdline] = cannedResponse{out: out, err: err}
}

func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *Fake) Runner() Runner { return &fakeRunner{f: f} }

type fakeRunner struct{ f *Fake }

func (r *fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	cmdline := name + " " + strings.Join(args, " ")
	cmdline = strings.TrimSpace(cmdline)
	r.f.mu.Lock()
	r.f.calls = append(r.f.calls, cmdline)
	resp, ok := r.f.canned[cmdline]
	r.f.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("fakeRunner: no canned response for %q", cmdline)
	}
	return resp.out, resp.err
}

func (r *fakeRunner) Run(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
	out, err := r.Output(ctx, name, args...)
	if stdout != nil && len(out) > 0 {
		_, _ = stdout.Write(out)
	}
	return err
}
