# config
`platform.json` نقطه‌ی سوار کردنه. schema: `contracts/config/v1/platform.config.schema.json`.

```json
{
  "version": 1,
  "default_timeout_ms": 500,
  "cores": [
    {"id": "a", "kind": "http", "options": {"url": "http://127.0.0.1:9001", "token_env": "A_TOKEN", "timeout_ms": 300}},
    {"id": "b", "kind": "<نوع-سفارشی>", "capabilities": ["x.v1.y"], "options": {}}
  ],
  "roles": {"sources": ["a", "b"]},
  "bindings": [
    {"capability": "x.v1.y", "cores": ["b", "a"]},
    {"capability": "x.v1.z", "cores": ["role:sources"], "mode": "fanout", "on_error": "skip", "timeout_ms": 800}
  ],
  "serve": {"listen": "127.0.0.1:8080", "token_env": "PLATFORM_TOKEN", "expose": ["x.v1.y"]},
  "extensions": {}
}
```
- `cores`: چه چیزی سوار می‌شه. `kind: "http"` داخلیه (هر هسته‌ی HTTP، هر زبان، یا یک پلتفرم دیگه). نوع‌های دیگه رو برنامه‌ی میزبان ثبت می‌کنه.
- `capabilities` خالی = از خود هسته کشف می‌شه (`core.v1.describe`). اگه پر باشه و هسته چیزی که ادعا شده رو نداشته باشه، **استارت خطا می‌ده**.
- `bindings`: ترتیب ترجیح هسته‌ها برای هر قابلیت؛ اولین هسته‌ی سالم جواب می‌ده. بدون binding، همه‌ی هسته‌های ارائه‌دهنده به ترتیب اعلام در نظر گرفته می‌شن.
- `roles`: چند هسته زیر یک اسم؛ binding با `"role:<نام>"` بهش اشاره می‌کنه (ترتیب حفظ می‌شه، هسته‌ی تکراری یک‌بار می‌مونه). نقش فقط گروه‌بندیه: تعویض هسته = تغییر یک خط. نقش خالی یا با هسته‌ی تعریف‌نشده خطاست.
- `bindings[].mode`: `single` (دقیقاً یک هسته، بدون fallback)، `fallback` (اولین هسته‌ی سالم)، `fanout` (همه هم‌زمان؛ فقط فراخوانی fan-out مجازه). خالی = رفتار پیش‌فرض (هر دو مجاز). `on_error` (فقط با fanout): `skip` یا `fail`. `timeout_ms`: مهلت این capability (در fanout مهلت هر منبع).
- `serve.expose`: **تنها** قابلیت‌هایی که بیرون می‌بینه (`"*"` = همه). خالی = فقط `core.v1.*`. قابلیتی که لیست نشده برای بیرون **دقیقاً شبیه قابلیتی که وجود نداره** است (۴۰۴ `unknown_capability`)، `core.v1.describe` فقط همین‌ها رو می‌گه و `core.v1.health` فقط status می‌ده (بدون اسم هسته‌ها). اسم اشتباه در `expose` موقع استارت خطاست. `/livez` باز و بی‌جزئیاته؛ `/healthz` (با جزئیات هسته‌ها) با توکن محافظت می‌شه.
- `token_env`: **اسم** متغیر محیطی (نه خود راز). اگه ست باشه و متغیر خالی باشه، استارت رد می‌شه.
- `extensions`: فضای آزاد برای هر چیزی که خودت روش سوار می‌کنی؛ engine هیچ‌وقت نمی‌خونتش.
- فیلد ناشناخته و ارجاع به هسته‌ی تعریف‌نشده خطاست.

## More keys (all optional, all have defaults; see `platform -print-config`)

```json
{
  "wrappers": [{"capability": "x.v1.y", "kind": "policy", "placement": "outer", "check_contract": true, "options": {}}],
  "subscriptions": [{"event": "x.v1.seen", "core": "role:sinks", "timeout_ms": 2000}],
  "bindings": [{"capability": "x.v1.y", "cores": ["a", "b"], "mode": "fallback", "retries": 1}],
  "resilience": {"retries": 0, "retry_delay_ms": 50, "breaker_failures": 5, "breaker_cooldown_ms": 30000},
  "limits": {"max_depth": 0},
  "contracts_dirs": ["../inner/contracts"],
  "cores": [{"id": "out", "kind": "gateway", "options": {"allowed_domains": ["api.example.com"], "keys": [{"domain": "api.example.com", "header": "Authorization", "env": "API_KEY", "prefix": "Bearer "}]}}]
}
```
