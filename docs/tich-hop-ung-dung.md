# Tích hợp subllm vào ứng dụng

Dành cho người viết ứng dụng **gọi vào** subllm (RAG, bot, script…). Cài đặt và
cấu hình phía server xem [README](../README.md).

subllm nói đúng giao thức OpenAI chat completions, nên client OpenAI nào cũng
dùng được: đổi base URL là xong. Dưới đây là từng luồng, kèm hành vi đã đo thật
ngày 28/10 trên provider kiểu `codex`.

## 0. Kết nối

| | giá trị |
|---|---|
| base URL | `http://<SUBLLM_HOST>:<SUBLLM_PORT>/v1` (mẫu `http://127.0.0.1:18080/v1`) |
| xác thực | server đặt `SUBLLM_API_KEY` → mọi endpoint trừ `/healthz` đòi `Authorization: Bearer <key>`, sai/thiếu → 401. Không đặt → không cần |
| chọn model | lấy `id` từ `GET /v1/models`; model được route theo tiền tố (`MODEL_PREFIX`) tới nhóm tài khoản tương ứng |

subllm chỉ nghe ở loopback: ứng dụng phải chạy cùng máy.

## 1. Hỏi – đáp text

```bash
curl -s http://127.0.0.1:18080/v1/chat/completions -H 'Content-Type: application/json' -d '{
  "model": "<model>",
  "messages": [
    {"role": "system", "content": "Trả lời ngắn gọn bằng tiếng Việt."},
    {"role": "user", "content": "1+1=?"}
  ]}'
```

Phản hồi đúng dạng OpenAI: `choices[0].message.content`, `usage`
(`prompt_tokens` / `completion_tokens` / `total_tokens`), `finish_reason` luôn
là `"stop"`.

Nhiều câu hỏi trong một lượt: mảng part text (xem mục 4) hoặc gộp vào một
chuỗi. Dặn rõ *"trả lời đủ N câu, đánh số"* để model không bỏ câu.

## 2. Hội thoại nhiều lượt

subllm **không lưu** hội thoại. Mỗi request gửi lại toàn bộ lịch sử theo thứ tự,
câu trả lời trước đặt ở vai `assistant`:

```json
"messages": [
  {"role": "system", "content": "…"},
  {"role": "user", "content": "câu 1"},
  {"role": "assistant", "content": "trả lời 1"},
  {"role": "user", "content": "câu 2, hỏi tiếp câu 1"}
]
```

Vai nhận được: `system`, `developer` (gộp vào chỉ dẫn hệ thống), `user`,
`assistant`. Vai khác (`tool`…) → 400. Ảnh/file ở lượt trước vẫn được model
"nhớ" nếu gửi lại cả tin nhắn chứa nó (đã đo: lượt sau hỏi lại mã trong ảnh của
lượt trước → đúng).

## 3. Stream

Thêm `"stream": true`. subllm trả SSE đúng dạng OpenAI:

```
data: {"id":"subllm-…","object":"chat.completion.chunk",…,"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}
data: {…"delta":{"content":"1"}…}
…
data: {…"delta":{},"finish_reason":"stop"}]}
data: [DONE]
```

- Lỗi **trước** chữ đầu tiên → mã HTTP thật (400/404/429/502), không phải 200.
- Lỗi **giữa chừng** (đã gửi chữ) → chunk cuối có `finish_reason: "error"`, rồi
  `[DONE]`. Client nên coi câu trả lời là dở dang.
- Stream **không** có `usage`. Cần đếm token thì gọi non-stream.

## 4. Ảnh và file

`content` của tin nhắn `user` là mảng part, trộn được nhiều text / ảnh / file:

```json
{"role": "user", "content": [
  {"type": "text", "text": "Câu 1: ảnh có gì?"},
  {"type": "image_url", "image_url": {"url": "data:image/png;base64,<...>"}},
  {"type": "text", "text": "Câu 2: file là hợp đồng số mấy?"},
  {"type": "file", "file": {"filename": "a.pdf", "file_data": "data:application/pdf;base64,<...>"}}
]}
```

Chi tiết, loại file đã thử và lỗi thường gặp: [anh-va-file.md](anh-va-file.md).

## 5. Mức suy nghĩ (`reasoning_effort`)

```json
{"model": "<model>", "reasoning_effort": "low", "messages": […]}
```

Chuyển nguyên cho backend; mức hợp lệ tuỳ model, sai → 400 kèm danh sách mức
backend nhận. Không gửi = mặc định của model. Provider kiểu `claude` chưa hỗ trợ
→ 400.

## 6. Chọn tài khoản và failover

- **Mặc định** (không gửi `provider`): subllm chọn tài khoản theo tiền tố model,
  dính 429 / lỗi tài khoản thì tự chuyển sang tài khoản kế trong nhóm. Ứng dụng
  không phải làm gì.
- **Chỉ định** `"provider": "<tên>"`: đúng tài khoản đó, không failover. Dùng
  khi cần cô lập một tài khoản (đo hạn mức, debug). Tên lấy từ `GET /status`.

## 7. Xử lý lỗi ở client

Body lỗi dạng OpenAI: `{"error": {"message": "…", "type": "…"}}`. Lỗi từ backend
thì `message` mở đầu bằng tên provider đã thử cuối.

| HTTP | `type` | nghĩa | client nên |
|---|---|---|---|
| 400 | `invalid_request_error` | request sai: thiếu `model`/`messages`, vai lạ, part chưa hỗ trợ, `reasoning_effort` sai, backend không tải được link ảnh | sửa request, **không** thử lại |
| 401 | `auth` | thiếu/sai API key | sửa key |
| 404 | `model_not_found` | model không khớp tiền tố nào, hoặc `provider` không tồn tại | kiểm `GET /v1/models` / `GET /status` |
| 405 | `invalid_request_error` | không dùng POST | dùng POST |
| 429 | `rate_limit_error` | mọi tài khoản của nhóm đang dính hạn mức | chờ đúng `Retry-After` (giây, luôn có) rồi thử lại |
| 502 | `upstream_error` | backend lỗi / mất mạng | thử lại vài lần có giãn cách; lặp lại thì xem log server |

Ví dụ client tối giản có tôn trọng `Retry-After` (chỉ dùng thư viện chuẩn
Python):

```python
import json, time, urllib.request, urllib.error

BASE = "http://127.0.0.1:18080/v1"

def chat(messages, model, retries=3, **extra):
    body = json.dumps({"model": model, "messages": messages, **extra}).encode()
    for attempt in range(retries):
        req = urllib.request.Request(BASE + "/chat/completions", body, {"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=300) as r:
                return json.load(r)["choices"][0]["message"]["content"]
        except urllib.error.HTTPError as e:
            if e.code == 429 and attempt < retries - 1:
                time.sleep(int(e.headers.get("Retry-After", "5")))
                continue
            if e.code == 502 and attempt < retries - 1:
                time.sleep(2 ** attempt)
                continue
            raise RuntimeError(f"HTTP {e.code}: {json.load(e)['error']['message']}") from None

print(chat([{"role": "user", "content": "1+1=?"}], model="<model>"))
```

## 8. Theo dõi

| endpoint | dùng để |
|---|---|
| `GET /healthz` | sống hay chết (`ok`) |
| `GET /v1/models` | danh sách model, `owned_by` = tên provider. Có `SUBLLM_API_KEY` thì đòi key |
| `GET /status` | từng tài khoản: `name`, `ok`, `failed`, `failovers`, `last_error` + `last_error_at`, `cooling_until` (đang nghỉ tới khi nào). Có `SUBLLM_API_KEY` thì đòi key |

Failover che lỗi khỏi ứng dụng, nên tài khoản hỏng chỉ thấy ở `/status` và log
server.

## 9. Field bị bỏ qua — cần biết

subllm chỉ đọc `model`, `messages`, `stream`, `provider`, `reasoning_effort`,
`max_tokens`. Mọi field khác **bị bỏ qua, không báo lỗi**. Đã đo:

| field | kết quả |
|---|---|
| `temperature`, `top_p` | HTTP 200, không có tác dụng |
| `max_tokens` với kiểu `codex` | bỏ qua: đặt 3 vẫn ra 20 completion_tokens (chỉ kiểu `claude` dùng) |
| `tools` / function calling | bỏ qua: model trả lời là không có công cụ nào, không bao giờ có `tool_calls` |

Cũng **không có**: embedding, rerank, `n` > 1, `logprobs`, `response_format`.

## 10. Provider kiểu `claude` — thử nghiệm

Code và unit test đã có (text, ảnh, PDF); đọc token và `/v1/models` chạy, nhưng
chat với tài khoản thử luôn nhận 429 từ backend nên **chưa chat được lượt nào**. Không nên dùng trong ứng dụng; nếu bật thì
để sau provider `codex` trong `SUBLLM_PROVIDERS`.
