# project (نام موقت؛ هر اسمی بذار)

یک پوشه‌ی اصلی؛ همه‌چیز داخلشه. **هیچ نمونه‌ای در ساختار نیست:** پوشه‌هایی که می‌تونن هر چیزی باشن خالین و جایگاهشون معلوم می‌کنه چی می‌تونه باشه.

```
project/
├── cores/                 هسته‌ها: هر هسته یک پوشه‌ی مستقل (هر زبان، هر فناوری، هر پلتفرم)
│   ├── project-1/         (خالی)
│   └── project-2/         (خالی)
│
├── app/                   هر چیزی که خودت اضافه می‌کنی (فرانت، بک‌اند، ...؛ ممکنه اصلاً هیچ‌کدوم نباشه)
│
├── config/
│   └── platform.json      نقطه‌ی سوار کردن: چه چیزی سوار باشه و کدوم هسته به کدوم قابلیت خدمت کنه
│
├── contracts/             قراردادهای تثبیت‌شده‌ی ساختاری (مستقل از زبان) + هر قرارداد دیگه‌ای که خودت می‌نویسی
│   ├── contract.schema.json       قالب فایل قرارداد
│   ├── core/v1/                   core.v1.describe و core.v1.health (هر هسته باید جواب بده)
│   ├── wire/v1/                   قالب درخواست/پاسخ بین همه‌ی اجزا
│   └── config/v1/                 schema پیکربندی
│
└── engine/                پایه‌ی کوچک و پایدار + برنامه‌ی عمومی + راستی‌آزمایی
    ├── base/              ثبت، اتصال، فراخوانی، boot از روی config، انتقال HTTP
    ├── cmd/platform/      برنامه‌ی عمومی: config را سوار می‌کند و از طریق قرارداد سرو می‌دهد
    ├── verify/            قوانین پوشه (archtest)، تست انطباق، fixtureهای تست
    ├── deploy/            Dockerfile و docker-compose.yml برای خود پلتفرم (هسته‌ها سرویس‌های جدا)
    └── docs/
```
(فایل‌های `.gitkeep` فقط برای نگه‌داشتن پوشه‌های خالی در git هستن؛ پاک‌شون کن اگه لازم نیست.)

## قوانین (با تست اجرا می‌شن)
1. هیچ هسته‌ای هسته‌ی دیگه یا `app/` رو نمی‌شناسه؛ فقط قرارداد.
2. هر پوشه‌ی `app/*` مستقله و مستقیم هسته‌ها رو import نمی‌کنه.
3. `engine/base` هیچ چیز بالاتر از خودش رو نمی‌شناسه و هیچ اسم ربات/پلتفرم/سرویس خاصی توش نیست.
4. ارتباط فقط از طریق قراردادهای نسخه‌دار.
5. بیرون فقط چیزی رو می‌بینه که در `serve.expose` لیست کردی (پیش‌فرض: هیچ‌چیز به‌جز `core.v1.*`).

## استفاده
```
go run ./engine/cmd/platform -config config/platform.json            # سرو روی 127.0.0.1:8080
docker compose -f engine/deploy/docker-compose.yml up --build        # اجرای پلتفرم در کانتینر
go run ./engine/cmd/platform -config config/platform.json -contracts contracts   # با قراردادهای خودت
go run ./engine/cmd/platform -config config/platform.json -print-config   # config نهایی + کلیدهایی که پیش‌فرض‌اند
go test ./...                                                        # راستی‌آزمایی (Go ≥ 1.22)
pip install jsonschema && python3 engine/verify/conformance/validate_structure.py contracts engine/verify/testdata/config
```
- **هسته‌ی جدید:** یک پوشه در `cores/`. هر زبانی؛ فقط باید فرمت سیم (`contracts/wire/v1`) و `core.v1.describe` / `core.v1.health` رو جواب بده. بعد در `config/platform.json` سوارش کن.
- **قابلیت جدید:** یک فایل `*.contract.json` در `contracts/<حوزه>/v1/`.
- **لایه‌ی رویی:** هر چیزی در `app/` که با قرارداد (HTTP+JSON) با پلتفرم حرف بزنه، به هر زبانی.

اسم ماژول `example.com/project` جایگزین‌شدنیه: `grep -rl example.com/project . | xargs sed -i 's#example.com/project#github.com/YOU/نام#g'`.
جزئیات: پوشه‌ی `docs/` (مستندات کامل، انگلیسی)، `config/README.md`.
