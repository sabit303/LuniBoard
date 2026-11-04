//go:build e2e

// Package e2e drives the real luniboard binary through its CLI:
// two daemons are started, paired desktop-to-desktop and exchange text.
// Run with: go test -tags e2e ./tests/e2e/
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "luniboard-e2e-bin-")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "luniboard")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/luniboard")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type instance struct {
	t    *testing.T
	home string
}

func newInstance(t *testing.T, name string) *instance {
	home, err := os.MkdirTemp("", "luniboard-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	inst := &instance{t: t, home: home}
	t.Cleanup(func() {
		_, _ = inst.run("stop")
		os.RemoveAll(home)
	})
	cfg := `[device]
name = "` + name + `"
[network]
listen_address = "127.0.0.1"
port = 0
[discovery]
enabled = false
[clipboard]
backend = "none"
[history]
persist = false
`
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config", "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return inst
}

func (i *instance) run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, append([]string{"--home", i.home}, args...)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func (i *instance) must(args ...string) string {
	i.t.Helper()
	out, err := i.run(args...)
	if err != nil {
		i.t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return out
}

func (i *instance) jsonOut(v any, args ...string) {
	i.t.Helper()
	out := i.must(append([]string{"--json"}, args...)...)
	if err := json.Unmarshal([]byte(out), v); err != nil {
		i.t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

func TestCLIPairSendHistory(t *testing.T) {
	a := newInstance(t, "Alpha")
	b := newInstance(t, "Beta")
	a.must("start")
	b.must("start")

	var inv struct {
		URI  string `json:"uri"`
		Code string `json:"code"`
	}
	a.jsonOut(&inv, "pair")
	if !strings.HasPrefix(inv.URI, "luniboard://pair?") {
		t.Fatalf("unexpected invitation %+v", inv)
	}

	connectDone := make(chan string, 1)
	go func() {
		out, err := b.run("pair", "connect", inv.URI)
		if err != nil {
			out = "ERROR: " + out
		}
		connectDone <- out
	}()

	var reqID string
	for i := 0; i < 100 && reqID == ""; i++ {
		var st struct {
			Pending []struct {
				ID               string `json:"id"`
				VerificationCode string `json:"verification_code"`
			} `json:"pending"`
		}
		a.jsonOut(&st, "pair", "status")
		if len(st.Pending) > 0 {
			reqID = st.Pending[0].ID
		}
		time.Sleep(100 * time.Millisecond)
	}
	if reqID == "" {
		t.Fatal("no pending pairing request")
	}
	a.must("pair", "approve", reqID)
	if out := <-connectDone; !strings.Contains(out, `Paired with "Alpha"`) {
		t.Fatalf("pair connect output:\n%s", out)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		out := a.must("devices")
		if strings.Contains(out, "Beta") && strings.Contains(out, "connected") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Beta never connected:\n%s", out)
		}
		time.Sleep(100 * time.Millisecond)
	}

	out := a.must("send", "--to", "Beta", "hello from the CLI")
	if !strings.Contains(out, "Beta: applied") {
		t.Fatalf("send output:\n%s", out)
	}
	if out := b.must("history", "--full"); !strings.Contains(out, "hello from the CLI") {
		t.Fatalf("history on Beta:\n%s", out)
	}
	if out := a.must("status"); !strings.Contains(out, "1 paired, 1 connected") {
		t.Fatalf("status:\n%s", out)
	}

	a.must("unpair", "Beta")
	if out := a.must("devices"); !strings.Contains(out, "No paired devices") {
		t.Fatalf("devices after unpair:\n%s", out)
	}
	a.must("stop")
	if _, err := a.run("status"); err == nil {
		t.Fatal("status succeeded after stop")
	}
}
