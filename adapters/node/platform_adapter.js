'use strict';
// Node adapter (no dependencies): serves the HTTP+JSON wire format of the platform.
const http = require('http');
const crypto = require('crypto');

class CoreError extends Error {
  constructor(code, message = '') { super(message || code); this.code = code; this.msg = message; }
}

class Core {
  constructor(name, { version = '1', tokenEnv } = {}) {
    this.name = name; this.version = version;
    this.token = tokenEnv ? (process.env[tokenEnv] || '') : '';
    this.handlers = new Map();
    this.health = () => ['ok', ''];
  }
  capability(name, fn) { this.handlers.set(name, fn); return this; }

  async call(cap, input, meta) {
    if (cap === 'core.v1.describe')
      return [200, { output: { name: this.name, version: this.version,
        capabilities: [...this.handlers.keys()].sort(), publishes: [], subscribes: [] } }];
    if (cap === 'core.v1.health') {
      const [status, detail] = this.health();
      return [200, { output: detail ? { status, detail } : { status } }];
    }
    const fn = this.handlers.get(cap);
    if (!fn) return [404, { error: { code: 'unknown_capability' } }];
    try { return [200, { output: await fn(input, meta) }]; }
    catch (e) {
      if (e instanceof CoreError) return [422, { error: { code: e.code, message: e.msg } }];
      return [500, { error: { code: 'internal' } }];
    }
  }

  serve(port = 9001, host = '127.0.0.1') {
    const send = (res, status, body) => {
      const data = JSON.stringify(body);
      res.writeHead(status, { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(data) });
      res.end(data);
    };
    const server = http.createServer((req, res) => {
      if (req.method !== 'POST' || req.url !== '/v1/call')
        return send(res, 404, { error: { code: 'unknown_capability' } });
      if (this.token) {
        const got = Buffer.from((req.headers.authorization || '').replace(/^Bearer /, ''));
        const want = Buffer.from(this.token);
        if (got.length !== want.length || !crypto.timingSafeEqual(got, want))
          return send(res, 401, { error: { code: 'unauthorized' } });
      }
      let size = 0; const chunks = [];
      req.on('data', c => { size += c.length; if (size > (1 << 20)) req.destroy(); else chunks.push(c); });
      req.on('end', async () => {
        let r;
        try { r = JSON.parse(Buffer.concat(chunks).toString()); if (typeof r.capability !== 'string') throw 0; }
        catch { return send(res, 400, { error: { code: 'invalid_input' } }); }
        const [status, body] = await this.call(r.capability, r.input ?? {}, r.meta || {});
        send(res, status, body);
      });
    });
    return server.listen(port, host);
  }
}

module.exports = { Core, CoreError };
