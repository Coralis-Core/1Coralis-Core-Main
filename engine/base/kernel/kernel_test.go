package kernel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"example.com/project/engine/base/capability"
	"example.com/project/engine/base/config"
)

type rec struct {
	name   string
	log    *[]string
	failOn bool
}

func (r *rec) Name() string { return r.name }
func (r *rec) Start(context.Context, *Host) error {
	if r.failOn {
		return errors.New("nope")
	}
	*r.log = append(*r.log, "start:"+r.name)
	return nil
}
func (r *rec) Stop(context.Context) error { *r.log = append(*r.log, "stop:"+r.name); return nil }

func TestStartInOrderStopInReverse(t *testing.T) {
	var log []string
	p := New(config.Config{}, nil)
	for _, n := range []string{"a", "b", "c"} {
		if err := p.AddCore(&rec{name: n, log: &log}); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.Stop(context.Background())
	if got := strings.Join(log, ","); got != "start:a,start:b,start:c,stop:c,stop:b,stop:a" {
		t.Fatal(got)
	}
}

func TestFailedStartRollsBackStartedCores(t *testing.T) {
	var log []string
	p := New(config.Config{}, nil)
	p.AddCore(&rec{name: "a", log: &log})
	p.AddCore(&rec{name: "bad", log: &log, failOn: true})
	p.AddCore(&rec{name: "never", log: &log})
	if err := p.Start(context.Background()); err == nil {
		t.Fatal("want error")
	}
	if got := strings.Join(log, ","); got != "start:a,stop:a" {
		t.Fatal(got)
	}
}

func TestDuplicateCoreAndLateAddRejected(t *testing.T) {
	var log []string
	p := New(config.Config{}, nil)
	p.AddCore(&rec{name: "a", log: &log})
	if err := p.AddCore(&rec{name: "a", log: &log}); err == nil {
		t.Fatal("duplicate core name accepted")
	}
	p.Start(context.Background())
	if err := p.AddCore(&rec{name: "late", log: &log}); err == nil {
		t.Fatal("adding after Start must be refused")
	}
}

func TestNewAppliesRolesAndPolicyFromConfig(t *testing.T) {
	cfg, err := config.Parse(strings.NewReader(`{"version":1,
	  "cores":[{"id":"a","kind":"k"},{"id":"b","kind":"k"},{"id":"c","kind":"k"}],
	  "roles":{"sources":["b","c"]},
	  "bindings":[
	    {"capability":"x.v1.all","cores":["role:sources","a"],"mode":"fanout","on_error":"fail","timeout_ms":250},
	    {"capability":"x.v1.plain","cores":["a"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	r := New(cfg, nil).Host().Router
	pol := r.PolicyOf("x.v1.all")
	if pol.Mode != capability.ModeFanout || pol.OnError != capability.OnErrorFail || pol.Timeout != 250*time.Millisecond {
		t.Fatalf("policy not applied: %+v", pol)
	}
	if got := r.PolicyOf("x.v1.plain"); got != (capability.Policy{}) {
		t.Fatalf("a plain binding must keep the lenient default, got %+v", got)
	}

	// role:sources must really have been expanded into the provider chain
	for _, id := range []string{"a", "b", "c"} {
		err := r.ProvideJSON(id, "x.v1.all", func(context.Context, []byte) ([]byte, error) { return []byte(`1`), nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(r.Chain("x.v1.all"), ","); got != "b,c,a" {
		t.Fatalf("chain = %q, want b,c,a (role expanded in place)", got)
	}
}
