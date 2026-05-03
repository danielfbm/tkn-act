package engine

import (
	"testing"
	"time"

	"github.com/danielfbm/tkn-act/internal/tektontypes"
)

func TestPipelineTimeoutsParse(t *testing.T) {
	cases := []struct {
		name     string
		spec     *tektontypes.Timeouts
		wantPipe time.Duration
		wantTask time.Duration
		wantFin  time.Duration
		wantErr  bool
	}{
		{"nil", nil, 0, 0, 0, false},
		{"empty", &tektontypes.Timeouts{}, 0, 0, 0, false},
		{"pipeline only", &tektontypes.Timeouts{Pipeline: "10m"}, 10 * time.Minute, 0, 0, false},
		{"all three", &tektontypes.Timeouts{Pipeline: "10m", Tasks: "8m", Finally: "2m"}, 10 * time.Minute, 8 * time.Minute, 2 * time.Minute, false},
		{"malformed pipeline", &tektontypes.Timeouts{Pipeline: "x"}, 0, 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePipelineTimeouts(tc.spec)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if got.Pipeline != tc.wantPipe || got.Tasks != tc.wantTask || got.Finally != tc.wantFin {
				t.Errorf("got %+v, want %v/%v/%v", got, tc.wantPipe, tc.wantTask, tc.wantFin)
			}
		})
	}
}

func TestPipelineTimeoutsBudgets(t *testing.T) {
	cases := []struct {
		name        string
		p           pipelineTimeouts
		wantTasks   time.Duration
		wantFinally time.Duration
	}{
		{"all zero", pipelineTimeouts{}, 0, 0},
		{"only pipeline", pipelineTimeouts{Pipeline: 10 * time.Minute}, 10 * time.Minute, 10 * time.Minute},
		{"pipeline + finally", pipelineTimeouts{Pipeline: 10 * time.Minute, Finally: 2 * time.Minute}, 8 * time.Minute, 2 * time.Minute},
		{"pipeline + tasks", pipelineTimeouts{Pipeline: 10 * time.Minute, Tasks: 7 * time.Minute}, 7 * time.Minute, 3 * time.Minute},
		{"all three", pipelineTimeouts{Pipeline: 10 * time.Minute, Tasks: 7 * time.Minute, Finally: 2 * time.Minute}, 7 * time.Minute, 2 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.tasksBudget(); got != tc.wantTasks {
				t.Errorf("tasks = %v, want %v", got, tc.wantTasks)
			}
			if got := tc.p.finallyBudget(); got != tc.wantFinally {
				t.Errorf("finally = %v, want %v", got, tc.wantFinally)
			}
		})
	}
}
