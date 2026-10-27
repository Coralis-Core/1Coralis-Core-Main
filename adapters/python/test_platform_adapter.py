import http.client, json, os, threading, time, unittest
from platform_adapter import Core, CoreError

def make_core():
    c = Core("t", token_env="T_TOKEN")
    @c.capability("x.v1.ok")
    def ok(i, m): return {"echo": i}
    @c.capability("x.v1.dom")
    def dom(i, m): raise CoreError("limit_exceeded", "no")
    @c.capability("x.v1.boom")
    def boom(i, m): raise RuntimeError("secret internals")
    return c

class Direct(unittest.TestCase):
    def setUp(self): self.c = make_core()
    def test_ok(self): self.assertEqual(self.c.call("x.v1.ok", {"a": 1}, {}), (200, {"output": {"echo": {"a": 1}}}))
    def test_domain_error(self): self.assertEqual(self.c.call("x.v1.dom", {}, {})[0:2], (422, {"error": {"code": "limit_exceeded", "message": "no"}}))
    def test_internal_hidden(self):
        st, body = self.c.call("x.v1.boom", {}, {})
        self.assertEqual((st, body), (500, {"error": {"code": "internal"}}))
        self.assertNotIn("secret", json.dumps(body))
    def test_unknown(self): self.assertEqual(self.c.call("nope.v1.x", {}, {})[0], 404)
    def test_describe_sorted(self):
        out = self.c.call("core.v1.describe", {}, {})[1]["output"]
        self.assertEqual(out["capabilities"], ["x.v1.boom", "x.v1.dom", "x.v1.ok"])
    def test_health(self): self.assertEqual(self.c.call("core.v1.health", {}, {})[1], {"output": {"status": "ok"}})

class Http(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        os.environ["T_TOKEN"] = "tok"
        cls.c = make_core()
        threading.Thread(target=cls.c.serve, kwargs={"port": 19101}, daemon=True).start()
        time.sleep(0.3)
    def post(self, body, token="tok", path="/v1/call", raw=None):
        h = http.client.HTTPConnection("127.0.0.1", 19101)
        hd = {"Authorization": "Bearer " + token} if token is not None else {}
        h.request("POST", path, raw if raw is not None else json.dumps(body), hd)
        r = h.getresponse(); return r.status, json.loads(r.read())
    def test_ok(self): self.assertEqual(self.post({"capability": "x.v1.ok", "input": {"a": 1}})[0], 200)
    def test_bad_token(self): self.assertEqual(self.post({"capability": "x.v1.ok"}, token="wrong")[0], 401)
    def test_no_token(self): self.assertEqual(self.post({"capability": "x.v1.ok"}, token=None)[0], 401)
    def test_bad_json(self): self.assertEqual(self.post(None, raw="{not json")[0], 400)
    def test_wrong_path(self): self.assertEqual(self.post({"capability": "x.v1.ok"}, path="/other")[0], 404)

if __name__ == "__main__": unittest.main()
