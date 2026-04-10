# worker-tunnel

<div dir="rtl" align="right">

`worker-tunnel` یک تونل TCP روی WebSocket است که کلاینت آن با Go خالص نوشته شده و سمت سرور آن روی Cloudflare Workers اجرا می‌شود.

## این پروژه چه کار می‌کند؟

- روی یک آدرس TCP محلی گوش می‌دهد
- چند اتصال WebSocket احراز هویت‌شده به Worker باز نگه می‌دارد
- اتصال‌های TCP ورودی را روی این WebSocketها multiplex می‌کند
- هر کانال را از سمت Worker به یک مقصد TCP دیگر وصل می‌کند

## ساختار ریپو

- `main.go`: کلاینت اصلی پروژه
- `worker.js`: کد Cloudflare Worker
- `.github/workflows/build.yml`: ورک‌فلو بیلد و انتشار فایل‌های اجرایی

## راه‌اندازی

### 1. دیپلوی Worker

فایل `worker.js` را روی Cloudflare Workers دیپلوی کنید. این Worker درخواست‌های WebSocket را می‌پذیرد و با استفاده از `cloudflare:sockets` به مقصد TCP وصل می‌شود.

برای احراز هویت، Worker هدر `Authorization` را بررسی می‌کند. اگر در محیط Cloudflare متغیر `AUTH_SECRET` تعریف شده باشد، همان استفاده می‌شود؛ در غیر این صورت مقدار پیش‌فرض داخل فایل به کار می‌رود.

### 2. بیلد کلاینت Go

</div>

```bash
go build -o worker-tunnel .
```

<div dir="rtl" align="right">

### 3. اجرای برنامه

</div>

```bash
AUTH_TOKEN="your-secret" \
SNI="your-worker.workers.dev" \
WORKER_IPS="204.12.196.34,63.141.252.203" \
PROXY_TARGET="1.1.1.1:443" \
LISTEN_ADDR="0.0.0.0:443" \
./worker-tunnel
```

<div dir="rtl" align="right">

## تنظیمات

کلاینت Go تنظیمات را از متغیرهای محیطی می‌خواند.

</div>

| متغیر | مقدار پیش‌فرض | توضیح |
| --- | --- | --- |
| `LISTEN_ADDR` | `0.0.0.0:443` | آدرس محلی برای listen کردن |
| `PROXY_TARGET` | `1.1.1.1:443` | مقصد TCP که Worker به آن وصل می‌شود |
| `WORKER_IPS` | `204.12.196.34,63.141.252.203` | لیست IPهای جداشده با ویرگول برای dial مستقیم |
| `SNI` | `*.workers.dev` | مقداری که برای TLS SNI و هدر `Host` استفاده می‌شود |
| `AUTH_TOKEN` | `d4TkphgXAsFk-Ij7APvyw3BPjOhQMDZcO9ngJ4o11wY` | توکن احراز هویت ارسالی به Worker |
| `BUFFER_SIZE` | `4096` | اندازه بافر خواندن TCP |
| `WORKERS_COUNT` | `5` | تعداد workerهای WebSocket که هم‌زمان زنده نگه داشته می‌شوند |
| `RECONNECT_AFTER_SEC` | `2` | فاصله زمانی برای reconnect بعد از قطع اتصال |
| `CONNECT_TIMEOUT_SEC` | `10` | timeout برای برقراری اتصال WebSocket |
| `TLS_SKIP_VERIFY` | وقتی `SNI` شامل `*` باشد به‌صورت پیش‌فرض `true` است، وگرنه `false` | کنترل بررسی گواهی TLS |

<div dir="rtl" align="right">

## نکات مهم

- کلاینت Go فقط از standard library استفاده می‌کند و وابستگی خارجی ندارد.
- اگر از `*.workers.dev` استفاده می‌کنید، بررسی TLS به‌صورت پیش‌فرض غیرفعال می‌شود. برای حالت مطمئن‌تر از یک hostname واقعی مثل `your-worker.workers.dev` استفاده کنید.
- اگر یکی از workerها قطع شود، همه کانال‌های متصل به همان worker بسته می‌شوند و کلاینت دوباره تلاش می‌کند اتصال را برقرار کند.
- در `worker.js` تبدیل base64 برای payloadهای بزرگ و مدیریت بستن socketها تقویت شده تا رفتار پایدارتر شود.

## بیلد و Release

GitHub Actions برای این معماری‌ها فایل اجرایی می‌سازد:

- Linux `amd64`
- Linux `arm64`
- macOS `amd64`
- macOS `arm64`
- Windows `amd64`
- Windows `arm64`

این workflow در این حالت‌ها اجرا می‌شود:

- هنگام `push` به `master`
- هنگام `pull_request`
- به‌صورت دستی با `workflow_dispatch`

اگر یک tag با الگوی `v*` بسازید، مثلاً `v1.0.0`، علاوه بر artifactهای معمول، فایل‌های اجرایی داخل GitHub Release همان نسخه هم قرار می‌گیرند.

برای انتشار نسخه:

</div>

```bash
git tag v1.0.0
git push origin v1.0.0
```

<div dir="rtl" align="right">

بعد از اجرای workflow، فایل‌های اجرایی در بخش Releases همان نسخه قابل دریافت خواهند بود.

</div>
