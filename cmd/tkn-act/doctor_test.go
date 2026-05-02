package main

import (
	"context"
	"encoding/json"
	"testing"
)

func TestDoctorReportShape(t *testing.T) {
	rep := buildDoctorReport(context.Background())
	if rep.OS == "" || rep.Arch == "" {
		t.Fatalf("OS/Arch not populated: %+v", rep)
	}
	if rep.CacheDir == "" {
		t.Fatalf("CacheDir not populated")
	}
	names := map[string]bool{}
	for _, c := range rep.Checks {
		names[c.Name] = true
	}
	for _, want := range []string{"cache_dir", "docker", "k3d", "kubectl"} {
		if !names[want] {
			t.Errorf("missing doctor check %q", want)
		}
	}
	// Must encode cleanly to JSON.
	if _, err := json.Marshal(rep); err != nil {
		t.Fatalf("marshal: %v", err)
	}
}

func TestDoctorRequiredForLabels(t *testing.T) {
	rep := buildDoctorReport(context.Background())
	required := map[string]string{
		"cache_dir": "default",
		"docker":    "default",
		"k3d":       "cluster",
		"kubectl":   "cluster",
	}
	for _, c := range rep.Checks {
		if want, ok := required[c.Name]; ok && c.RequiredFor != want {
			t.Errorf("%s.RequiredFor = %q, want %q", c.Name, c.RequiredFor, want)
		}
	}
}
