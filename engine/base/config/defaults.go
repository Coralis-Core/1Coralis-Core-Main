package config

// This file is the ONE place where defaults live. Every key has a default, so
// a project works with no config at all and you only set what you need.
// `platform -print-config` shows the final config and which values came from
// here.
const (
	DefaultTimeoutMS       = 10000            // default_timeout_ms: per-call budget
	DefaultListen          = "127.0.0.1:8080" // serve.listen
	DefaultRetries         = 0                // resilience.retries: repeating is only safe if the contract says idempotent
	DefaultRetryDelayMS    = 50               // resilience.retry_delay_ms
	DefaultBreakerFailures = 5                // resilience.breaker_failures (-1 = off)
	DefaultBreakerCooldown = 30000            // resilience.breaker_cooldown_ms
	DefaultMaxDepth        = 0                // limits.max_depth: 0 = unlimited (protections are off until you turn them on)
	// Not numbers, but defaults all the same:
	//   bindings[].mode            "" = lenient (single-result and fan-out calls both allowed)
	//   bindings[].on_error        skip (fan-out only)
	//   wrappers[].placement       outer
	//   wrappers[].check_contract  true
	//   subscriptions[].timeout_ms 2000
	//   serve.expose               nothing but core.v1.*
	//   gateway options            strict: no domain allowed, no redirects, private addresses blocked,
	//                              1 MiB, 10 s (see engine/base/gateway)
)

// WithDefaults returns the config with every unset key filled in, plus the
// dotted names of the keys that were filled from defaults.
func (c Config) WithDefaults() (Config, []string) {
	var from []string
	mark := func(key string) { from = append(from, key) }
	if c.DefaultTimeoutMS == 0 {
		c.DefaultTimeoutMS = DefaultTimeoutMS
		mark("default_timeout_ms")
	}
	if c.Serve.Listen == "" {
		c.Serve.Listen = DefaultListen
		mark("serve.listen")
	}
	if c.Resilience.RetryDelayMS == 0 {
		c.Resilience.RetryDelayMS = DefaultRetryDelayMS
		mark("resilience.retry_delay_ms")
	}
	if c.Resilience.BreakerFailures == 0 {
		c.Resilience.BreakerFailures = DefaultBreakerFailures
		mark("resilience.breaker_failures")
	}
	if c.Resilience.BreakerCooldown == 0 {
		c.Resilience.BreakerCooldown = DefaultBreakerCooldown
		mark("resilience.breaker_cooldown_ms")
	}
	// retries and max_depth default to 0 on purpose; they are listed so the
	// final config is complete.
	if c.Resilience.Retries == 0 {
		mark("resilience.retries")
	}
	if c.Limits.MaxDepth == 0 {
		mark("limits.max_depth")
	}
	for i := range c.Wrappers {
		if c.Wrappers[i].Placement == "" {
			c.Wrappers[i].Placement = PlaceOuter
			mark(sprintIdx("wrappers", i, "placement"))
		}
		if c.Wrappers[i].CheckContract == nil {
			t := true
			c.Wrappers[i].CheckContract = &t
			mark(sprintIdx("wrappers", i, "check_contract"))
		}
	}
	for i := range c.Bindings {
		if c.Bindings[i].Mode == ModeFanout && c.Bindings[i].OnError == "" {
			c.Bindings[i].OnError = OnErrorSkip
			mark(sprintIdx("bindings", i, "on_error"))
		}
	}
	for i := range c.Subscriptions {
		if c.Subscriptions[i].TimeoutMS == 0 {
			c.Subscriptions[i].TimeoutMS = 2000
			mark(sprintIdx("subscriptions", i, "timeout_ms"))
		}
	}
	return c, from
}

func sprintIdx(section string, i int, key string) string {
	return section + "[" + itoa(i) + "]." + key
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}
