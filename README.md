# worker-tunnel

`worker-tunnel` نسخه‌ی Go خالص برای جایگزینی `tunnel.py` قدیمی است. این پروژه همان ایده‌ی قبلی را نگه می‌دارد:

- روی یک آدرس محلی TCP گوش می‌دهد
- چند اتصال WebSocket احراز هویت‌شده به Cloudflare Worker باز می‌کند
- چند کانال TCP را روی همان WebSocketها multiplex می‌کند
- هر کانال را از سمت Worker به یک مقصد TCP دیگر وصل می‌کند

فایل `worker.js` همچنان سمت Cloudflare Worker این تونل را پیاده‌سازی می‌کند.

## ساختار ریپو

- `main.go`: کلاینت اصلی با Go
- `worker.js`: کد Cloudflare Worker برای اتصال WebSocket به TCP
- `.github/workflows/build.yml`: ورک‌فلو GitHub Actions برای ساخت فایل‌های اجرایی

## شروع سریع

### 1. دیپلوی Worker

فایل `worker.js` را با Cloudflare Workers دیپلوی کنید؛ فرقی ندارد با `wrangler` باشد، پنل Cloudflare یا CI.

این Worker فعلاً این هدرها را بررسی می‌کند:

- `Upgrade: websocket`
- `Authorization`

اگر خواستی، می‌توانی سکرت را به‌جای هاردکد از `env.AUTH_SECRET` در Cloudflare هم پاس بدهی؛ کد همین حالا از آن پشتیبانی می‌کند.

### 2. بیلد باینری Go

```bash
go build -o worker-tunnel .
```

### 3. اجرای برنامه

```bash
AUTH_TOKEN="your-secret" \
SNI="your-worker.workers.dev" \
WORKER_IPS="204.12.196.34,63.141.252.203" \
PROXY_TARGET="1.1.1.1:443" \
LISTEN_ADDR="0.0.0.0:443" \
./worker-tunnel
```

## تنظیمات

کلاینت Go تنظیمات را از environment variableها می‌خواند.

| متغیر | مقدار پیش‌فرض | توضیح |
| --- | --- | --- |
| `LISTEN_ADDR` | `0.0.0.0:443` | آدرس محلی برای listen کردن |
| `PROXY_TARGET` | `1.1.1.1:443` | مقصد TCP که Worker به آن وصل می‌شود |
| `WORKER_IPS` | `204.12.196.34,63.141.252.203` | لیست IPهای جداشده با ویرگول برای dial مستقیم |
| `SNI` | `*.workers.dev` | مقداری که برای TLS SNI و هدر `Host` استفاده می‌شود |
| `AUTH_TOKEN` | `d4TkphgXAsFk-Ij7APvyw3BPjOhQMDZcO9ngJ4o11wY` | توکن احراز هویت ارسالی به Worker |
| `BUFFER_SIZE` | `4096` | اندازه بافر خواندن TCP |
| `WORKERS_COUNT` | `5` | تعداد WebSocket workerهایی که هم‌زمان زنده نگه داشته می‌شوند |
| `RECONNECT_AFTER_SEC` | `2` | فاصله زمانی برای reconnect بعد از قطع اتصال |
| `CONNECT_TIMEOUT_SEC` | `10` | timeout برای برقراری اتصال WebSocket |
| `TLS_SKIP_VERIFY` | وقتی `SNI` شامل `*` باشد به‌صورت پیش‌فرض `true` است، وگرنه `false` | کنترل بررسی گواهی TLS |

## نکات مهم

- کلاینت Go فقط با standard library نوشته شده و وابستگی جانبی ندارد.
- اگر `SNI=*.workers.dev` بماند، بررسی TLS به‌صورت پیش‌فرض غیرفعال می‌شود، چون wildcard literal برای verify مناسب نیست. برای حالت امن‌تر از یک hostname واقعی مثل `my-worker.workers.dev` استفاده کن.
- اگر یک worker قطع شود، همه کانال‌های متصل به همان worker بسته می‌شوند؛ این رفتار با نسخه پایتون قبلی هماهنگ است.
- در `worker.js` تبدیل base64 برای payloadهای بزرگ امن‌تر شده و مدیریت بستن socketها هم کامل‌تر شده تا نشت resource کمتر شود.

## GitHub Actions

ورک‌فلو موجود برای این پلتفرم‌ها فایل اجرایی می‌سازد و به‌عنوان artifact آپلود می‌کند:

- Linux `amd64`
- Linux `arm64`
- macOS `amd64`
- macOS `arm64`
- Windows `amd64`
- Windows `arm64`

این ورک‌فلو در این حالت‌ها اجرا می‌شود:

- روی هر `push`
- روی `pull_request`
- به‌صورت دستی با `workflow_dispatch`
