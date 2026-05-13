package docker

import "testing"

// TestDecideRemote covers every branch of decideRemote that does not
// require a real daemon: the "on"/"off" forces, the unix-socket short
// circuit, an empty DOCKER_HOST, and the invalid-mode error path. The
// "auto + tcp" branch that calls cli.Info() is exercised indirectly by
// the docker_integration tests and (eventually) by the
// remote-docker-integration workflow.
func TestDecideRemote(t *testing.T) {
	cases := []struct {
		name       string
		mode       string
		dockerHost string
		wantRemote bool
		wantErr    bool
	}{
		{name: "force on", mode: "on", wantRemote: true},
		{name: "force off", mode: "off", wantRemote: false},
		{name: "auto + unix scheme is local", mode: "auto", dockerHost: "unix:///var/run/docker.sock", wantRemote: false},
		{name: "empty mode = auto + unix is local", mode: "", dockerHost: "unix:///var/run/docker.sock", wantRemote: false},
		{name: "auto + empty DOCKER_HOST is local", mode: "auto", dockerHost: "", wantRemote: false},
		{name: "invalid mode errors", mode: "yes", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// cli is nil — none of these branches dereference it.
			remote, err := decideRemote(tc.mode, tc.dockerHost, nil)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got remote=%v nil", remote)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if remote != tc.wantRemote {
				t.Errorf("remote = %v, want %v", remote, tc.wantRemote)
			}
		})
	}
}
