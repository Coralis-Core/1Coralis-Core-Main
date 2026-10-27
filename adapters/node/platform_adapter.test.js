'use strict';
const test = require('node:test');
const assert = require('node:assert');
const http = require('http');
const { Core, CoreError } = require('./platform_adapter.js');

process.env.T_TOKEN = 'tok';
const c = new Core('t', { tokenEnv: 'T_TOKEN' });
c.capability('x.v1.ok', (i) => ({ echo: i }));
c.capability('x.v1.dom', () => { throw new CoreError('limit_exceeded', 'no'); });
c.capability('x.v1.boom', () => { throw new Error('secret internals'); });

const post = (body, { token = 'tok', path = '/v1/call', raw } = {}) => new Promise((resolve, reject) => {
  const data = raw ?? JSON.stringify(body);
  const headers = token === null ? {} : { Authorization: 'Bearer ' + token };
  const r = http.request({ host: '127.0.0.1', port: 19102, path, method: 'POST', headers }, (res) => {
    let s = ''; res.on('data', d => s += d); res.on('end', () => resolve([res.statusCode, JSON.parse(s)]));
  });
  r.on('error', reject); r.end(data);
});

let server;
test.before(() => { server = c.serve(19102); });
test.after(() => server.close());

test('ok', async () => assert.deepStrictEqual(await post({ capability: 'x.v1.ok', input: { a: 1 } }), [200, { output: { echo: { a: 1 } } }]));
test('domain error -> 422', async () => assert.deepStrictEqual(await post({ capability: 'x.v1.dom' }), [422, { error: { code: 'limit_exceeded', message: 'no' } }]));
test('internal error hidden', async () => {
  const [st, body] = await post({ capability: 'x.v1.boom' });
  assert.strictEqual(st, 500); assert.ok(!JSON.stringify(body).includes('secret'));
});
test('unknown capability', async () => assert.strictEqual((await post({ capability: 'nope.v1.x' }))[0], 404));
test('describe sorted', async () => assert.deepStrictEqual((await post({ capability: 'core.v1.describe' }))[1].output.capabilities, ['x.v1.boom', 'x.v1.dom', 'x.v1.ok']));
test('bad token', async () => assert.strictEqual((await post({ capability: 'x.v1.ok' }, { token: 'wrong' }))[0], 401));
test('no token', async () => assert.strictEqual((await post({ capability: 'x.v1.ok' }, { token: null }))[0], 401));
test('bad json', async () => assert.strictEqual((await post(null, { raw: '{nope' }))[0], 400));
test('wrong path', async () => assert.strictEqual((await post({ capability: 'x.v1.ok' }, { path: '/other' }))[0], 404));
