# engine
پایه‌ی کوچک و پایدار: ثبت، پیدا کردن، اتصال و فراخوانی. هیچ منطق دامنه، هیچ فناوری مشخص و هیچ اسم ربات/پلتفرم.

- `base/`: `capability` (روتر با fallback و fan-out)، `kernel`، `boot` (ساخت از config)، `config`، `contract`، `jsonschema`، `transports/httpjson`، `bus`.
- `cmd/platform/`: برنامه‌ی عمومی (config ← سوار کردن ← سرو).
- `verify/`: `archtest` (قوانین پوشه)، `conformance` (تست انطباق و تست‌های سوار کردن)، `fixtures` (نمونه‌های فقط-برای-تست)، `testdata`.
- `docs/`.

افزودن هسته، قرارداد یا چیزی در `app/` نباید نیاز به تغییر این پوشه داشته باشه.
