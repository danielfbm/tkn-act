package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/danielfbm/tkn-act/internal/exitcode"
	"github.com/docker/docker/client"
	"github.com/spf13/cobra"
)

type doctorCheck struct {
	Name        string `json:"name"`
	OK          bool   `json:"ok"`
	Detail      string `json:"detail,omitempty"`
	RequiredFor string `json:"required_for,omitempty"` // "default" or "cluster"
}

type doctorReport struct {
	OK       bool          `json:"ok"`
	Version  string        `json:"version"`
	OS       string        `json:"os"`
	Arch     string        `json:"arch"`
	CacheDir string        `json:"cache_dir"`
	Checks   []doctorCheck `json:"checks"`
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose the local environment for tkn-act",
		Long: `Verify the environment is set up to run pipelines:
Docker daemon (required), k3d and kubectl (required for --cluster), cache dir,
and version. Returns exit code 3 if a required check fails.

For AI agents: prefer 'tkn-act doctor -o json' for a stable, parseable report.`,
		Example: `  tkn-act doctor
  tkn-act doctor -o json`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rep := buildDoctorReport(context.Background())
			if gf.output == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(rep)
			} else {
				printDoctorReport(rep)
			}
			if !rep.OK {
				return exitcode.Wrap(exitcode.Env, fmt.Errorf("environment check failed"))
			}
			return nil
		},
	}
}

func buildDoctorReport(ctx context.Context) doctorReport {
	rep := doctorReport{
		Version:  version,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		CacheDir: cacheDir(),
	}

	rep.Checks = append(rep.Checks, checkCacheDir(rep.CacheDir))
	rep.Checks = append(rep.Checks, checkDocker(ctx))
	rep.Checks = append(rep.Checks, checkBinaryOnPath("k3d", "cluster"))
	rep.Checks = append(rep.Checks, checkBinaryOnPath("kubectl", "cluster"))

	rep.OK = true
	for _, c := range rep.Checks {
		if !c.OK && c.RequiredFor == "default" {
			rep.OK = false
		}
	}
	return rep
}

func checkCacheDir(path string) doctorCheck {
	c := doctorCheck{Name: "cache_dir", RequiredFor: "default"}
	if err := os.MkdirAll(path, 0o755); err != nil {
		c.Detail = fmt.Sprintf("cannot create %s: %v", path, err)
		return c
	}
	probe := filepath.Join(path, ".doctor-write-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		c.Detail = fmt.Sprintf("not writable: %v", err)
		return c
	}
	_ = os.Remove(probe)
	c.OK = true
	c.Detail = path
	return c
}

func checkDocker(ctx context.Context) doctorCheck {
	c := doctorCheck{Name: "docker", RequiredFor: "default"}
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		c.Detail = err.Error()
		return c
	}
	defer func() { _ = cli.Close() }()

	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	ping, err := cli.Ping(pctx)
	if err != nil {
		c.Detail = err.Error()
		return c
	}
	c.OK = true
	c.Detail = fmt.Sprintf("API %s", ping.APIVersion)
	return c
}

func checkBinaryOnPath(name, requiredFor string) doctorCheck {
	c := doctorCheck{Name: name, RequiredFor: requiredFor}
	p, err := exec.LookPath(name)
	if err != nil {
		c.Detail = "not found on PATH"
		return c
	}
	c.OK = true
	if v, err := versionOf(p); err == nil && v != "" {
		c.Detail = v
	} else {
		c.Detail = p
	}
	return c
}

func versionOf(bin string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").CombinedOutput()
	if err != nil {
		// kubectl needs --client to avoid contacting the cluster
		out, err = exec.CommandContext(ctx, bin, "version", "--client").CombinedOutput()
		if err != nil {
			return "", err
		}
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if len(line) > 120 {
		line = line[:120]
	}
	return line, nil
}

func printDoctorReport(r doctorReport) {
	fmt.Printf("tkn-act %s on %s/%s\n", r.Version, r.OS, r.Arch)
	fmt.Println("cache dir:", r.CacheDir)
	for _, c := range r.Checks {
		mark := "OK "
		if !c.OK {
			mark = "FAIL"
		}
		req := ""
		if c.RequiredFor != "" {
			req = " (required for: " + c.RequiredFor + ")"
		}
		detail := ""
		if c.Detail != "" {
			detail = " — " + c.Detail
		}
		fmt.Printf("  [%s] %s%s%s\n", mark, c.Name, req, detail)
	}
	if r.OK {
		fmt.Println("\nready to run pipelines")
	} else {
		fmt.Println("\none or more required checks failed")
	}
}
