// verify: one command that checks everything the author could not check, on YOUR machine
// (Windows/Linux/macOS, no bash needed). It prints PASS/FAIL/SKIP per step, writes verify.log,
// and for each failure prints the last lines of that step so you can paste the report back.
//
//	go run ./scripts/verify                          main module + adapters + live demo + gRPC (+ Docker if present)
//	go run ./scripts/verify -core-bin ./mycore       also starts YOUR built core executable (it must implement the
//	                                                 three counter.v1.* contracts in domain-contracts/ and read CORE_TOKEN
//	                                                 and LISTEN=host:port or HOST/PORT from the environment), mounts it and
//	                                                 calls it through the platform. It WRITES then DELETES one test record
//	                                                 (counter name "verify-probe") in whatever that core stores.
//	go run ./scripts/verify -core-bin X -core-dir D  same, with D as the core's working directory (for a .env, say)
//	go run ./scripts/verify -docker-up               also runs `docker compose up` and smoke-tests it
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type step struct {
	name, status, note string
	logFrom, logTo     int64
}

var (
	steps []step
	logF  *os.File
	root  string
)

func logf(format string, a ...any) { fmt.Fprintf(logF, format, a...) }
func pos() int64                   { p, _ := logF.Seek(0, io.SeekCurrent); return p }

func record(name, status, note string, from int64) {
	steps = append(steps, step{name, status, note, from, pos()})
	fmt.Printf("%-5s %s %s\n", status, name, note)
}

// run executes a command, appending it and its output to the log.
func run(dir string, env []string, name string, args ...string) error {
	logf("\n$ (%s) %s %s\n", dir, name, strings.Join(args, " "))
	c := exec.Command(name, args...)
	c.Dir = dir
	c.Env = append(os.Environ(), env...)
	c.Stdout, c.Stderr = logF, logF
	return c.Run()
}

func check(name, dir string, env []string, cmd string, args ...string) bool {
	from := pos()
	if err := run(dir, env, cmd, args...); err != nil {
		record(name, "FAIL", err.Error(), from)
		return false
	}
	record(name, "PASS", "", from)
	return true
}

func have(bin string) bool { _, err := exec.LookPath(bin); return err == nil }

// realPython returns a working Python. On Windows "python3"/"python" can be the Microsoft Store
// stub, which exists on PATH but is not Python, so each candidate must actually print its version.
func realPython() string {
	for _, c := range []string{"python3", "python"} {
		if !have(c) {
			continue
		}
		if o, err := exec.Command(c, "--version").CombinedOutput(); err == nil && strings.HasPrefix(string(o), "Python 3") {
			return c
		}
	}
	return ""
}

func raceArgs() []string {
	out, _ := exec.Command("go", "env", "CGO_ENABLED").Output()
	if strings.TrimSpace(string(out)) == "1" && (have("gcc") || have("cc") || have("clang")) {
		return []string{"-race"}
	}
	return nil
}

func freePort() string {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	return fmt.Sprint(l.Addr().(*net.TCPAddr).Port)
}

func waitHTTP(url string, d time.Duration) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if r, err := http.Get(url); err == nil {
			r.Body.Close()
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func waitTCP(addr string, d time.Duration) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond); err == nil {
			c.Close()
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func call(base, capName, input string) (int, map[string]any, error) {
	body := fmt.Sprintf(`{"capability":%q,"input":%s}`, capName, input)
	resp, err := http.Post(base+"/v1/call", "application/json", strings.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	var m map[string]any
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &m)
	logf("call %s %s -> %d %s\n", capName, input, resp.StatusCode, bytes.TrimSpace(b))
	return resp.StatusCode, m, nil
}

func out(m map[string]any) map[string]any { o, _ := m["output"].(map[string]any); return o }

// e2e starts a core process, the platform binary, and drives the three counter contracts.
func e2e(name, platformBin, corePort string, core *exec.Cmd, wipe bool) {
	from := pos()
	fail := func(note string) { record(name, "FAIL", note, from) }
	core.Stdout, core.Stderr = logF, logF
	logf("\n$ start core: %s %s\n", core.Path, strings.Join(core.Args[1:], " "))
	if err := core.Start(); err != nil {
		fail("core did not start: " + err.Error())
		return
	}
	defer func() { core.Process.Kill(); core.Wait() }()
	if !waitTCP("127.0.0.1:"+corePort, 20*time.Second) {
		fail("core never listened on port " + corePort + " (see log)")
		return
	}
	pport := freePort()
	tmp, _ := os.MkdirTemp("", "verify")
	defer os.RemoveAll(tmp)
	dc, _ := filepath.Abs(filepath.Join(root, "domain-contracts"))
	caps := `["counter.v1.increment","counter.v1.get","counter.v1.reset"]`
	cfg := fmt.Sprintf(`{"version":1,"contracts_dirs":[%q],"cores":[{"id":"core","kind":"http","capabilities":%s,
	 "options":{"url":"http://127.0.0.1:%s","token_env":"CORE_TOKEN"}}],
	 "serve":{"listen":"127.0.0.1:%s","expose":%s}}`, dc, caps, corePort, pport, caps)
	cfgPath := filepath.Join(tmp, "platform.json")
	os.WriteFile(cfgPath, []byte(cfg), 0o600)
	pl := exec.Command(platformBin, "-config", cfgPath)
	pl.Env = os.Environ()
	pl.Stdout, pl.Stderr = logF, logF
	if err := pl.Start(); err != nil {
		fail("platform did not start: " + err.Error())
		return
	}
	defer func() { pl.Process.Kill(); pl.Wait() }()
	base := "http://127.0.0.1:" + pport
	if !waitHTTP(base+"/livez", 15*time.Second) {
		fail("platform never answered /livez")
		return
	}
	const id = `{"name":"verify-probe"`
	if wipe {
		call(base, "counter.v1.reset", id+`}`)
	}
	st, m, err := call(base, "counter.v1.increment", id+`}`)
	if err != nil || st != 200 || out(m)["value"] != float64(1) {
		fail(fmt.Sprintf("increment: want 200 and value 1, got %d %v %v", st, m, err))
		return
	}
	st, m, _ = call(base, "counter.v1.get", id+`}`)
	if st != 200 || out(m)["value"] != float64(1) {
		fail(fmt.Sprintf("get: want value 1, got %d %v", st, m))
		return
	}
	if st, m, _ = call(base, "counter.v1.increment", id+`,"by":5000}`); st != 422 {
		fail(fmt.Sprintf("a domain error (limit_exceeded) must be 422, got %d %v", st, m))
		return
	}
	st, m, _ = call(base, "counter.v1.reset", id+`}`)
	if st != 200 || out(m)["removed"] != true {
		fail(fmt.Sprintf("reset: want removed true, got %d %v", st, m))
		return
	}
	if st, _, _ = call(base, "counter.v1.increment", `{}`); st != 400 {
		fail(fmt.Sprintf("invalid input must be 400, got %d", st))
		return
	}
	record(name, "PASS", "", from)
}

func main() {
	coreBin := flag.String("core-bin", "", "path to a built external core executable to test end to end (opt-in; writes then deletes one test record)")
	coreDir := flag.String("core-dir", "", "working directory for -core-bin (default: the folder containing it)")
	dockerUp := flag.Bool("docker-up", false, "also run docker compose up and smoke-test it")
	flag.Parse()
	root, _ = filepath.Abs(".")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		fmt.Println("run this from the project root (the folder with go.mod)")
		os.Exit(2)
	}
	var err error
	if logF, err = os.OpenFile("verify.log", os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o644); err != nil {
		fmt.Println(err)
		os.Exit(2)
	}
	defer logF.Close()
	race := raceArgs()
	logf("os=%s/%s race=%v\n", runtime.GOOS, runtime.GOARCH, len(race) > 0)
	for _, t := range [][]string{{"go", "version"}, {"python3", "--version"}, {"python", "--version"}, {"node", "--version"}, {"docker", "--version"}} {
		if have(t[0]) {
			run(root, nil, t[0], t[1:]...)
		}
	}
	if !have("go") {
		fmt.Println("Go is not on PATH")
		os.Exit(2)
	}

	// 1. main module
	if check("main module: go build", root, nil, "go", "build", "./...") {
		check("main module: go vet", root, nil, "go", "vet", "./...")
		check("main module: go test"+map[bool]string{true: " -race", false: ""}[len(race) > 0], root, nil, "go", append(append([]string{"test", "-count=1"}, race...), "./...")...)
	}

	// 2. adapters
	py := realPython()
	if py != "" {
		check("python adapter tests", filepath.Join(root, "adapters", "python"), nil, py, "-m", "unittest", "test_platform_adapter")
	} else {
		record("python adapter tests", "SKIP", "python not found", pos())
	}
	if have("node") {
		check("node adapter tests", filepath.Join(root, "adapters", "node"), nil, "node", "--test")
	} else {
		record("node adapter tests", "SKIP", "node not found", pos())
	}

	// 3. live demo: platform binary + a real core process
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	bin := filepath.Join(os.TempDir(), "verify-platform"+exe)
	if check("build platform binary", root, nil, "go", "build", "-o", bin, "./engine/cmd/platform") {
		cport := freePort()
		env := append(os.Environ(), "CORE_TOKEN=verify-token", "PORT="+cport, "HOST=127.0.0.1", "LISTEN=127.0.0.1:"+cport)
		switch {
		case py != "":
			c := exec.Command(py, filepath.Join(root, "examples", "counter-demo-python", "main.py"))
			c.Env = env
			os.Setenv("CORE_TOKEN", "verify-token")
			e2e("live demo (platform + python core)", bin, cport, c, false)
		case have("node"):
			c := exec.Command("node", filepath.Join(root, "examples", "counter-demo-node", "main.js"))
			c.Env = env
			os.Setenv("CORE_TOKEN", "verify-token")
			e2e("live demo (platform + node core)", bin, cport, c, false)
		default:
			record("live demo", "SKIP", "needs python or node", pos())
		}
	}

	// 4. gRPC module (separate module; needs network the first time)
	g := filepath.Join(root, "engine", "contrib", "grpc")
	if check("grpc: go mod tidy (needs internet)", g, nil, "go", "mod", "tidy") {
		check("grpc: go vet", g, nil, "go", "vet", "./...")
		check("grpc: go test"+map[bool]string{true: " -race", false: ""}[len(race) > 0], g, nil, "go", append(append([]string{"test", "-count=1"}, race...), "./...")...)
		fmt.Println("      -> commit engine/contrib/grpc/go.sum")
	}

	// 5. docker
	if have("docker") {
		check("docker build", root, nil, "docker", "build", "-f", "engine/deploy/Dockerfile", "-t", "platform:verify", ".")
		check("docker compose config", filepath.Join(root, "engine", "deploy"), []string{"CORE_TOKEN=x", "PLATFORM_TOKEN=y"}, "docker", "compose", "config", "-q")
		if *dockerUp {
			dockerSmoke()
		}
	} else {
		record("docker build / compose", "SKIP", "docker not found", pos())
	}

	// 6. an external core of yours (opt-in)
	if *coreBin != "" {
		externalCheck(*coreBin, *coreDir, bin)
	} else {
		record("external core end to end", "SKIP", "pass -core-bin <your built core executable>", pos())
	}

	// report
	fmt.Println("\n================ REPORT (paste this) ================")
	bad := 0
	for _, s := range steps {
		fmt.Printf("%-5s %s %s\n", s.status, s.name, s.note)
		if s.status == "FAIL" {
			bad++
		}
	}
	for _, s := range steps {
		if s.status != "FAIL" {
			continue
		}
		fmt.Printf("\n--- last lines of: %s ---\n", s.name)
		b := make([]byte, s.logTo-s.logFrom)
		logF.ReadAt(b, s.logFrom)
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(lines) > 30 {
			lines = lines[len(lines)-30:]
		}
		fmt.Println(strings.Join(lines, "\n"))
	}
	fmt.Println("\nfull log: verify.log")
	if bad > 0 {
		os.Exit(1)
	}
}

func externalCheck(bin, dir, platformBin string) {
	from := pos()
	name := "external core end to end"
	bin, _ = filepath.Abs(bin)
	if st, err := os.Stat(bin); err != nil || st.IsDir() {
		record(name, "FAIL", "not an executable file: "+bin, from)
		return
	}
	if dir == "" {
		dir = filepath.Dir(bin)
	}
	dir, _ = filepath.Abs(dir)
	cport := freePort()
	os.Setenv("CORE_TOKEN", "verify-token")
	c := exec.Command(bin)
	c.Dir = dir
	c.Env = append(os.Environ(), "CORE_TOKEN=verify-token", "LISTEN=127.0.0.1:"+cport, "PORT="+cport, "HOST=127.0.0.1")
	e2e(name, platformBin, cport, c, true)
}

func dockerSmoke() {
	from := pos()
	d := filepath.Join(root, "engine", "deploy")
	env := []string{"CORE_TOKEN=verify-core-token", "PLATFORM_TOKEN=verify-platform-token"}
	if err := run(d, env, "docker", "compose", "up", "-d", "--build"); err != nil {
		record("docker compose up", "FAIL", err.Error(), from)
		return
	}
	defer run(d, env, "docker", "compose", "down", "-v")
	if !waitHTTP("http://127.0.0.1:8080/livez", 60*time.Second) {
		record("docker compose up", "FAIL", "platform never answered", from)
		return
	}
	req, _ := http.NewRequest("POST", "http://127.0.0.1:8080/v1/call", strings.NewReader(`{"capability":"counter.v1.increment","input":{"name":"verify-probe"}}`))
	req.Header.Set("Authorization", "Bearer verify-platform-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		record("docker compose up", "FAIL", fmt.Sprintf("authorized call failed: %v %v", err, resp), from)
		return
	}
	resp.Body.Close()
	record("docker compose up (platform + isolated core)", "PASS", "", from)
}
