package main

import (
	"testing"

	"github.com/danielfbm/tkn-act/internal/cluster/tekton"
)

func TestResolveTektonVersion(t *testing.T) {
	cases := []struct {
		name  string
		flag  string
		env   string
		want  string
	}{
		{"flag wins over env and default", "v1.3.0", "v1.6.0", "v1.3.0"},
		{"env wins when flag empty", "", "v1.6.0", "v1.6.0"},
		{"default when neither set", "", "", tekton.DefaultTektonVersion},
		{"empty env treated as unset", "", "", tekton.DefaultTektonVersion},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envTektonVersion, tc.env)
			if got := resolveTektonVersion(tc.flag); got != tc.want {
				t.Errorf("resolveTektonVersion(%q) with env=%q = %q, want %q",
					tc.flag, tc.env, got, tc.want)
			}
		})
	}
}
