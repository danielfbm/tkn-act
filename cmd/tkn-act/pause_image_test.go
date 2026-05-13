package main

import "testing"

func TestResolvePauseImage(t *testing.T) {
	cases := []struct {
		name string
		flag string
		env  string
		want string
	}{
		{"empty flag and env returns empty", "", "", ""},
		{"flag wins over env", "registry.local/pause:3.9", "ignored.example/pause:1", "registry.local/pause:3.9"},
		{"env honored when flag empty", "", "mirror.internal/pause:3.9", "mirror.internal/pause:3.9"},
		{"flag wins even when both set", "flag.example/pause:edge", "env.example/pause:edge", "flag.example/pause:edge"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envPauseImage, tc.env)
			got := resolvePauseImage(tc.flag)
			if got != tc.want {
				t.Errorf("resolvePauseImage(%q) with env=%q = %q, want %q", tc.flag, tc.env, got, tc.want)
			}
		})
	}
}
