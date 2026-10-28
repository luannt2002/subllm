# Gửi ảnh và file qua subllm

subllm nhận **text + ảnh + file** theo đúng định dạng content-parts của OpenAI
chat completions: `content` là một **mảng part**, trộn bao nhiêu text / ảnh /
file trong một tin nhắn cũng được, model đọc theo đúng thứ tự. `content` dạng
chuỗi như cũ vẫn chạy.

## Ba loại part

| part | dạng | ghi chú |
|---|---|---|
| text | `{"type": "text", "text": "..."}` | nhiều part text = nhiều câu hỏi trong một lượt |
| ảnh | `{"type": "image_url", "image_url": {"url": "<data URI hoặc link>"}}` | data URI: `data:image/png;base64,<...>` · link: backend tự tải, phải tải được |
| file | `{"type": "file", "file": {"filename": "a.pdf", "file_data": "data:application/pdf;base64,<...>"}}` | chỉ nhận `file_data` (base64), **không** nhận `file_id` |

Ảnh và file chỉ đặt trong tin nhắn `user`. `system` / `assistant` chỉ nhận
text: gửi ảnh vào đó → 400.

## Ví dụ 1 — dán vào Postman (ảnh bằng link)

`POST http://127.0.0.1:18080/v1/chat/completions`, header
`Content-Type: application/json` (thay `<model>` bằng một `id` lấy từ `GET /v1/models`, cả ở ví dụ 2):

```json
{
    "model": "<model>",
    "messages": [
        { "role": "system", "content": "Trả lời ngắn gọn bằng tiếng Việt." },
        { "role": "user", "content": [
            { "type": "text", "text": "Câu 1: 1+1=?" },
            { "type": "text", "text": "Câu 2: ảnh này là logo gì?" },
            { "type": "image_url", "image_url": { "url": "https://raw.githubusercontent.com/github/explore/main/topics/python/python.png" } }
        ]}
    ]
}
```

Kết quả chạy 28/10: HTTP 200, *"Câu 1: 1 + 1 = 2. Câu 2: Đây là logo Python."*

## Ví dụ 2 — gửi file có sẵn trên máy (ảnh, PDF, Word, Excel…)

Đổi câu hỏi và danh sách file ở dòng đầu. Đoạn này tự đổi file sang base64,
ảnh thành part ảnh, còn lại thành part file. Body đi qua stdin nên file lớn
không vướng giới hạn độ dài tham số dòng lệnh.

```bash
python3 - "Mỗi file có nội dung gì? Đánh số theo file." anh1.png anh2.jpg hopdong.pdf <<'EOF' | curl -s http://127.0.0.1:18080/v1/chat/completions -H 'content-type: application/json' -d @- | jq -r '.choices[0].message.content // .'
import sys, json, base64, mimetypes
question, files = sys.argv[1], sys.argv[2:]
parts = [{"type": "text", "text": question}]
for f in files:
    mt = mimetypes.guess_type(f)[0] or "application/octet-stream"
    url = f"data:{mt};base64," + base64.b64encode(open(f, "rb").read()).decode()
    if mt.startswith("image/"):
        parts.append({"type": "image_url", "image_url": {"url": url}})
    else:
        parts.append({"type": "file", "file": {"filename": f.split("/")[-1], "file_data": url}})
print(json.dumps({"model": "<model>", "messages": [{"role": "user", "content": parts}]}))
EOF
```

Muốn stream thì thêm `"stream": true` vào body, ảnh/file vẫn chạy như thường.

## Loại nào chạy được — bảng thử 28/10

Thử qua `127.0.0.1:18080`, provider kiểu `codex`. Mỗi file chứa
một mã riêng (vd `JPG-5821`), script tự kiểm câu trả lời có chép đúng mã không.

| ca | kết quả | prompt_tokens | thời gian |
|---|---|---|---|
| PNG | ✅ đọc đúng mã | 241 | 1,6 s |
| JPG | ✅ | 241 | 4,1 s |
| WEBP | ✅ | 241 | 1,9 s |
| GIF | ✅ | 241 | 2,3 s |
| JPG lớn 4000×3000, 11 MB | ✅ | 14159 | 9,5 s |
| PDF có lớp chữ, 2 trang (mã ở trang 2) | ✅ | 121 | 2,3 s |
| PDF scan (chỉ ảnh, không có chữ) | ✅ | 1003 | 3,4 s |
| TXT | ✅ | 78 | 2,1 s |
| CSV | ✅ | 100 | 4,3 s |
| DOCX (Word) | ✅ | 91 | 4,8 s |
| XLSX (Excel) | ✅ | 100 | 4,2 s |
| 6 câu hỏi + 4 ảnh + 2 PDF trong một lượt | ✅ 6/6 khi dặn *"Trả lời ĐỦ 6 câu"* · ⚠️ không dặn thì model chỉ trả lời câu cuối | 1905 | 5,2 s |
| stream + ảnh | ✅ | — | 1,7 s |
| nhiều lượt: lượt sau hỏi lại ảnh của lượt trước | ✅ | 230 | 1,7 s |
| ảnh bằng link GitHub raw | ✅ | 118 | 1,4 s |
| ảnh bằng link Wikimedia | ❌ backend không tải được link (Wikimedia chặn) | — | — |
| chỉ text, 2 câu trong một tin nhắn | ✅ | 36 | 1,6 s |
| tài khoản Codex thứ hai + ảnh + PDF | ✅ | — | — |
| provider `claude` + ảnh | ❌ chưa thử được: tài khoản Claude đang 429 | — | — |

## Mẹo

- **Nhiều câu hỏi một lượt:** dặn rõ ở đầu *"Có N câu, trả lời đủ N câu, đánh
  số"*. Không dặn thì model có thể chỉ trả lời câu cuối, dù đủ mọi phần đã tới
  backend (prompt_tokens vẫn đủ).
- **Ảnh lớn tốn token:** ảnh 11 MB tốn 14159 prompt_tokens, ảnh nhỏ tốn khoảng
  241. Thu nhỏ ảnh trước khi gửi nếu không cần chi tiết.
- **Link ảnh:** backend tự tải link; host chặn trình tải tự động (vd Wikimedia)
  thì lỗi. Chắc ăn nhất là gửi base64 (ví dụ 2).

## Lỗi thường gặp

| HTTP | thông báo | nghĩa |
|---|---|---|
| 400 | `part "input_audio" chưa hỗ trợ (chỉ text, image_url, file)` | gửi loại part subllm chưa dịch (âm thanh…) |
| 400 | `file cần file_data (data URI base64); file_id chưa hỗ trợ` | gửi `file_id` thay vì nội dung file |
| 400 | `image_url.url trống` | part ảnh thiếu `url` |
| 400 | `role "system" chỉ nhận text, không nhận image` | ảnh/file đặt ngoài tin nhắn `user` |
| 400 | `Error while downloading file. Upstream status code: …` | backend không tải được link ảnh (host chặn, link chết): gửi base64 thay link |

## Chưa hỗ trợ / chưa thử

- Chưa hỗ trợ: âm thanh (`input_audio`), video, `file_id`, trường `detail` của
  ảnh (bị bỏ qua, backend dùng mức mặc định).
- Provider `claude`: đã có code + unit test, chưa chạy thật (429). File base64 ở
  Claude chỉ nhận PDF.
- Trần body của một request: 64 MiB (base64 làm file to thêm khoảng 1/3) → quá thì 413.
- Chưa thử: file lớn hơn 11 MB, file loại khác ngoài bảng trên.
