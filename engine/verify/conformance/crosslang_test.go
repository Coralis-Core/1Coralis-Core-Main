package conformance

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"testing"

	"example.com/project/engine/base/contract"
	"example.com/project/engine/verify/fixtures/contracts"
)

// pipeProvider turns a line-oriented JSON process into a contract.Provider.
func pipeProvider(t *testing.T, capability string, w io.Writer, r *bufio.Reader) contract.Provider {
	return func(_ context.Context, input []byte) ([]byte, string, error) {
		req, _ := json.Marshal(map[string]any{"capability": capability, "input": json.RawMessage(input)})
		if _, err := w.Write(append(req, '\n')); err != nil {
			return nil, "", err
		}
		line, err := r.ReadBytes('\n')
		if err != nil {
			return nil, "", err
		}
		var resp struct {
			Output json.RawMessage `json:"output"`
			Error  *struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(line, &resp); err != nil {
			return nil, "", err
		}
		if resp.Error != nil {
			return nil, resp.Error.Code, nil
		}
		return resp.Output, "", nil
	}
}

// The Python core has never seen a Go type. It passes the same examples.
func TestPythonCoreConformsToContracts(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	set, err := contract.Load(contracts.FS)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(py, "../fixtures/cores/python/core.py")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { stdin.Close(); cmd.Wait() }()
	rd := bufio.NewReader(stdout)

	for _, name := range []string{"identity.v1.user.get", "identity.v1.chat.get"} {
		c, _ := set.Get(name)
		if problems := contract.Check(context.Background(), c, pipeProvider(t, name, stdin, rd)); len(problems) > 0 {
			t.Errorf("python core not conformant: %v", problems)
		}
	}
}

// Prove the harness has teeth: a wrong provider must be caught.
func TestCheckCatchesNonConformantProvider(t *testing.T) {
	set, _ := contract.Load(contracts.FS)
	c, _ := set.Get("identity.v1.user.get")
	wrong := func(context.Context, []byte) ([]byte, string, error) {
		return []byte(`{"id":"7"}`), "", nil // id must be an integer
	}
	if problems := contract.Check(context.Background(), c, wrong); len(problems) == 0 {
		t.Fatal("non-conformant provider was not detected")
	}
	noErr := func(context.Context, []byte) ([]byte, string, error) { return nil, "oops", nil }
	if problems := contract.Check(context.Background(), c, noErr); len(problems) == 0 {
		t.Fatal("wrong error code was not detected")
	}
}
