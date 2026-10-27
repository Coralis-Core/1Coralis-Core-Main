'use strict';
// Demo core (Node, no dependencies): named counters in memory, implementing the counter.v1.* contracts.
const path = require('path');
const { Core, CoreError } = require(path.join(__dirname, '..', '..', 'adapters', 'node', 'platform_adapter.js'));
const MAX = 1000;
const core = new Core('counter-demo', { tokenEnv: 'CORE_TOKEN' });
const counters = new Map();
core.capability('counter.v1.increment', (i) => {
  const value = (counters.get(i.name) || 0) + (i.by ?? 1);
  if (value > MAX) throw new CoreError('limit_exceeded', `a counter cannot exceed ${MAX}`);
  counters.set(i.name, value);
  return { value };
});
core.capability('counter.v1.get', (i) => ({ value: counters.get(i.name) || 0 }));
core.capability('counter.v1.reset', (i) => ({ removed: counters.delete(i.name) }));
core.serve(Number(process.env.PORT || 9001), process.env.HOST || '127.0.0.1');
