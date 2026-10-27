package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestCustomHostEndToEnd(t *testing.T) {
	p, set, err := build(platformJSON, os.DirFS("contracts"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer p.Stop(context.Background())
	srv := httptest.NewServer(p.Handler(set, "", nil))
	defer srv.Close()

	post := func(body string) (int, string) {
		resp, err := http.Post(srv.URL+"/v1/call", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b := new(strings.Builder)
		buf := make([]byte, 512)
		for {
			n, e := resp.Body.Read(buf)
			b.Write(buf[:n])
			if e != nil {
				break
			}
		}
		return resp.StatusCode, strings.TrimSpace(b.String())
	}

	if st, body := post(`{"capability":"greet.v1.hello","input":{"name":"Sara"}}`); st != 200 || body != `{"output":{"message":"Hello, Sara!"}}` {
		t.Fatalf("success: %d %s", st, body)
	}
	if st, _ := post(`{"capability":"greet.v1.hello","input":{"name":""}}`); st != 400 {
		t.Fatalf("a contract-violating input must be 400, got %d", st)
	}
	if st, _ := post(`{"capability":"core.v1.health","input":{}}`); st != 200 {
		t.Fatalf("structural capabilities are always exposed, got %d", st)
	}
	if st, _ := post(`{"capability":"nope.v1.x","input":{}}`); st != 404 {
		t.Fatalf("unknown capability must be 404, got %d", st)
	}
}
