package docker

import (
	"archive/tar"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestDirChainTar_WorldWritable: remote-mode staging dirs are created
// by the stager as root; they must be 0o777 (Tekton emptyDir mode) so
// a step image with a non-root USER can write results / workspaces.
func TestDirChainTar_WorldWritable(t *testing.T) {
	buf, err := dirChainTar("results/tr-1/steps/build")
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(buf)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if h.Typeflag != tar.TypeDir || h.Mode != 0o777 {
			t.Errorf("%s: type=%c mode=%o, want dir 0777", h.Name, h.Typeflag, h.Mode)
		}
	}
	if len(names) != 4 || names[3] != "results/tr-1/steps/build/" {
		t.Errorf("entries = %q", names)
	}
}

func TestTarHostDir_MissingDirIsWorldWritable(t *testing.T) {
	r, err := tarHostDir(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := tar.NewReader(r).Next()
	if err != nil {
		t.Fatal(err)
	}
	if h.Mode != 0o777 {
		t.Errorf("placeholder mode = %o, want 777", h.Mode)
	}
}

func TestMkdirWorldWritable(t *testing.T) {
	d := filepath.Join(t.TempDir(), "a", "b")
	if err := mkdirWorldWritable(d); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(d)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o777 {
		t.Errorf("mode = %o, want 777", st.Mode().Perm())
	}
}
