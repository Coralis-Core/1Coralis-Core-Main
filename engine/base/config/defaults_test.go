package config

import (
	"strings"
	"testing"
)

func TestEmptyConfigIsCompleteWithDefaults(t *testing.T) {
	c, err := Parse(strings.NewReader(`{"version":1}`))
	if err != nil {
		t.Fatal(err)
	}
	eff, from := c.WithDefaults()
	if eff.DefaultTimeoutMS != DefaultTimeoutMS || eff.Serve.Listen != DefaultListen ||
		eff.Resilience.BreakerFailures != DefaultBreakerFailures || eff.Limits.MaxDepth != 0 {
		t.Fatalf("%+v", eff)
	}
	if len(from) < 5 {
		t.Fatalf("every default must be reported: %v", from)
	}
}

func TestExplicitValuesWinAndListedDefaultsAreWhatWasFilled(t *testing.T) {
	c, _ := Parse(strings.NewReader(`{"version":1,"default_timeout_ms":99,"cores":[{"id":"a","kind":"http"}],
	 "bindings":[{"capability":"x.v1.y","cores":["a"],"mode":"fanout"}],
	 "wrappers":[{"capability":"x.v1.y","kind":"k","check_contract":false}],"resilience":{"breaker_failures":-1}}`))
	eff, from := c.WithDefaults()
	if eff.DefaultTimeoutMS != 99 || eff.Resilience.BreakerFailures != -1 || *eff.Wrappers[0].CheckContract {
		t.Fatalf("explicit values must win: %+v", eff)
	}
	joined := strings.Join(from, ",")
	for _, want := range []string{"bindings[0].on_error", "wrappers[0].placement"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%s should be reported as defaulted: %v", want, from)
		}
	}
	for _, not := range []string{"default_timeout_ms", "resilience.breaker_failures", "wrappers[0].check_contract"} {
		if strings.Contains(joined, not) {
			t.Errorf("%s was set explicitly, must not be reported: %v", not, from)
		}
	}
}
