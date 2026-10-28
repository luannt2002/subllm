# subllm — gói subscription thành OpenAI API cục bộ

[![ci](https://github.com/luannt2002/subllm/actions/workflows/ci.yml/badge.svg)](https://github.com/luannt2002/subllm/actions/workflows/ci.yml)

subllm mở API tương thích OpenAI (`/v1/chat/completions`, `/v1/models`) trên
`<SUBLLM_HOST>:<SUBLLM_PORT>` (mẫu `127.0.0.1:18080`, chỉ loopback), đi qua gói đăng ký (subscription) của bạn bằng token đăng nhập của
CLI chính thức. Ứng dụng nào nói được OpenAI API (curl, SDK, RAG…) đều dùng
được: đổi base URL là xong. Một binary Go, không dependency ngoài stdlib.

> ⚠️ **Không chính thức.** Dùng token của gói subscription ngoài CLI chính thức
> có thể trái điều khoản dịch vụ của nhà cung cấp; tài khoản có thể bị hạn chế
> hoặc khoá. Backend đổi là subllm phải sửa theo. Tự cân nhắc trước khi dùng.

Mỗi **provider** là **một tài khoản** của một **kiểu** (giao thức) mà code biết nói:

| kiểu (`TYPE`) | backend | token (`CRED_FILE`) | gói |
|---|---|---|---|
| `codex` | Codex backend (Responses API) | file auth của Codex CLI | ChatGPT Plus/Pro |
| `claude` | Anthropic Messages API (OAuth) | file credential của Claude Code | Claude Pro/Max — **thử nghiệm**, xem [Giới hạn](#giới-hạn) |

Viết ứng dụng gọi vào subllm: [docs/tich-hop-ung-dung.md](docs/tich-hop-ung-dung.md)
(từng luồng: text, hội thoại, stream, ảnh/file, lỗi). Ảnh và file:
[docs/anh-va-file.md](docs/anh-va-file.md).

## Chạy nhanh

Cần Go ≥ 1.22 và đã đăng nhập CLI chính thức một lần (vd `codex login`) để có
file credential.

```bash
git clone https://github.com/luannt2002/subllm && cd subllm
go build -o bin/subllm .
cp -n .env.example .env && chmod 600 .env    # rồi điền các khoá 🔒 (xem dưới)
./bin/subllm                                  # đọc .env ở thư mục hiện tại
```

Không cần clone: `go install github.com/luannt2002/subllm@latest` cài binary vào
`$(go env GOPATH)/bin`; chạy ở thư mục có `.env` (mẫu lấy từ `.env.example`)
hoặc chỉ file bằng `-env <đường dẫn>`.

**Các khoá 🔒 lấy ở đâu.** Repo cố ý không ghi sẵn giá trị của nhà cung cấp
(để đổi được khi backend đổi, và repo không mang dữ liệu của bên thứ ba):
- `SUBLLM_PROVIDERS` — mẫu có cả `codex` và `claude`; chỉ có một gói thì bỏ
  cái kia (cùng khối khoá của nó không cần điền).
- `CRED_FILE` — file mà CLI chính thức ghi sau khi đăng nhập.
- `BASE_URL`, `TOKEN_URL`, `CLIENT_ID`, `CLIENT_VERSION`, `BETA`,
  `USER_AGENT`, `API_VERSION` — giá trị CLI chính thức đang dùng, lấy từ mã
  nguồn / cấu hình của CLI đó.
- `MODEL_PREFIX` — tiền tố tên model của gói: đặt tạm, chạy, xem
  `GET /v1/models` (danh sách lấy thẳng từ backend) rồi đặt lại cho đúng.

Gọi thử:

```bash
curl http://127.0.0.1:18080/v1/models
curl http://127.0.0.1:18080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "<model>", "messages": [{"role": "user", "content": "Xin chào"}]}'
```

Chạy nền, chạy port khác, dừng:

```bash
(umask 077; setsid ./bin/subllm > /tmp/subllm.log 2>&1 &)  # log có đường dẫn file credential: file log mới tạo chỉ bạn đọc
SUBLLM_PORT=19000 ./bin/subllm                              # port khác mà không sửa .env — dừng bản kia trước
pkill -x subllm                                             # dừng
```

Dừng (SIGTERM / Ctrl-C) là **tắt êm**: ngừng nhận request mới, chờ request đang
chạy xong — kể cả lượt refresh token đang dở — rồi mới thoát; gửi tín hiệu lần
nữa để thoát ngay. Flag duy nhất: `-env`; mọi cấu hình khác qua env (key không
đi qua flag để khỏi lộ cho mọi user trên máy qua `ps`).

## Giới hạn

Đã kiểm thật (10/2026):

- **Không có embedding/rerank**: gói subscription chỉ mở model chat. Codex
  backend trả HTTP 400 *"… model is not supported when using Codex with a
  ChatGPT account"* cho model embedding; Anthropic `/v1/embeddings` và
  `/v1/rerank` đều 404. Embedding cho RAG phải lấy từ dịch vụ embedding riêng
  hoặc model chạy local.
- **Đầu vào: text, ảnh, file** (part `text` · `image_url` · `file` với
  `file_data`); part khác (`input_audio`, `file_id`…) → 400, không lặng lẽ bỏ.
  Kiểu `codex` đã chạy thật (nhiều ảnh + PDF một lượt); kiểu `claude` mới có
  unit test, và file base64 ở Claude chỉ nhận PDF. Bảng loại file đã thử:
  [docs/anh-va-file.md](docs/anh-va-file.md).
- **Không tools/function calling**: chỉ text out, đủ cho ứng dụng hỏi–đáp/RAG
  (agent cần tool chưa dùng subllm làm model chính được). Field `tools`,
  `temperature`… bị bỏ qua không báo lỗi — danh sách đã đo ở mục 9 của
  [docs/tich-hop-ung-dung.md](docs/tich-hop-ung-dung.md).
- **Kiểu `claude` là thử nghiệm**: đọc token và `/v1/models` chạy, nhưng chat
  (`/messages`) với tài khoản thử luôn nhận 429 `rate_limit_error` không kèm
  header hạn mức, kể cả tài khoản không có phiên Claude Code nào dùng — chưa
  rõ nguyên nhân, chưa chat được lượt nào. Muốn tắt: bỏ provider kiểu
  `claude` khỏi `SUBLLM_PROVIDERS` trong `.env`.
- Quirks đã biết của từng backend nằm trong `internal/provider/codex` /
  `internal/provider/claude`, có chú thích; giá trị đổi được nằm ở `.env`.

## Nhiều tài khoản + failover

- Route theo **tiền tố model** (`MODEL_PREFIX`). Nhiều provider cùng tiền tố =
  một nhóm tài khoản, thử **theo thứ tự** trong `SUBLLM_PROVIDERS`.
- Tài khoản dính **429** → nghỉ theo `Retry-After` của upstream (không có thì
  `error.resets_in_seconds` trong body 429 của Codex, không có nữa thì
  `SUBLLM_COOLDOWN`), request chuyển sang tài khoản kế. Lỗi của riêng tài khoản
  (**401, 403**, file credential hỏng, refresh bị từ chối) → cũng chuyển,
  nhưng không bắt nghỉ.
- **Không** chuyển khi: request sai (400), backend chung sập (5xx, mất mạng),
  hoặc stream **đã chảy dở** (không đổi tài khoản giữa câu được).
- Mọi tài khoản của nhóm đang nghỉ → trả **429 + `Retry-After`** ngay, không
  gọi upstream, để client tự lùi.
- Field `"provider": "<tên>"` trong body = chọn đích danh: đúng tài khoản đó,
  không failover, bỏ qua giờ nghỉ.
- `GET /status` — số đếm từng tài khoản (`name`, `ok`, `failed`, `failovers`,
  `last_error` + `last_error_at`, `cooling_until` — chỉ có khi đang nghỉ). Giờ
  nghỉ chỉ kéo dài, không bị 429 sau ngắn hơn rút lại. Failover che lỗi khỏi
  client, nên tài khoản chết chỉ thấy được ở đây và ở log (`failover A → B: …`).
  Có `SUBLLM_API_KEY` thì `/status` và `/v1/models` cũng đòi key (lộ tên tài
  khoản).

## Token — cần biết

**Ai giữ refresh token — đọc trước khi thêm tài khoản.** Refresh token xoay
vòng: hai nơi cùng refresh một token là một bên văng đăng nhập.

- Mỗi file credential chỉ một provider giữ — trùng file thì subllm dừng lúc
  khởi động.
- **Mỗi file credential chỉ một tiến trình subllm.** Khoá refresh nằm trong
  tiến trình, **không** khoá chung với Claude Code / Codex CLI: hai bản subllm
  (hoặc subllm + CLI) cùng refresh một token thì bản sau bị từ chối. Tài khoản
  thêm vào nhóm nên là tài khoản **không** đang được CLI dùng.
- subllm **đọc** file credential của CLI chính thức, tự refresh khi còn hạn
  dưới `REFRESH_MARGIN` và **ghi ngược lại đúng file đó** (tệp tạm + fsync +
  rename, 0600) — CLI đọc cùng file thì hưởng token tươi. Hạn của token opaque
  lấy từ `expires_in` của phản hồi refresh. File credential là symlink → ghi
  vào file thật, symlink giữ nguyên.
- Refresh chạy đến cùng dù client đã huỷ request: server có thể đã đổi token,
  bỏ dở là file giữ token chết. Refresh lỗi mà token cũ còn hạn → dùng tạm
  token cũ (log ghi lại), request sau thử lại. Refresh xong mà ghi file lỗi
  (đĩa đầy, thư mục không ghi được) → giữ token mới trong bộ nhớ, request sau
  dùng nó và ghi lại. Log báo "giữ token mới trong bộ nhớ" thì sửa quyền
  ghi / đĩa **trước khi** dừng subllm — dừng lúc đó là mất token mới.
- `codex login` / `claude login` lại khi refresh bị từ chối (`/status` và log
  ghi lỗi). Refresh token đã bị từ chối (400/401/403) không được gửi lại cho
  tới khi file có token khác: khi token cũ hết hạn, request báo lỗi ngay, không
  chờ mạng (trước đó vẫn dùng tạm token cũ, chỉ log ghi lỗi).

## Cấu hình — không hard code, không fallback

Mọi giá trị chạy nằm ở env, đọc **một chỗ** là `config.go`; mẫu ở
[.env.example](.env.example). Provider tên `<N>` kiểu `<T>` đọc key `<K>` lần
lượt ở:

```
SUBLLM_PROVIDER_<N>_<K>     riêng provider (tài khoản) đó
SUBLLM_TYPE_<T>_<K>         chung cho mọi provider cùng kiểu
SUBLLM_<K>                  chung cho mọi provider
```

- Mọi biến đều bắt buộc (trừ `SUBLLM_API_KEY`). Thiếu hoặc **sai** — URL
  không phải http/https, port ngoài 1–65535, thời lượng hỏng, host không phải
  loopback, TYPE lạ, tên provider trùng, file credential dùng chung, khoá lặp
  trong `.env`, khoá `.env` không hợp lệ (vd `export X=…`), URL có
  `?`/`#` — là subllm dừng lúc khởi động và liệt kê đủ một lượt. Code
  không có giá trị mặc định nào để "đỡ".
- Ngoại lệ có chủ ý: file credential **chưa có** lúc khởi động chỉ in cảnh
  báo, không dừng — CLI login / công cụ đổi tài khoản tạo hoặc dời file sau.
  Request tới tài khoản đó lỗi rõ ràng (có tên file) và failover sang tài khoản kế.
- So trùng file credential theo đường dẫn tuyệt đối, đã giải symlink (cả
  thư mục cha), và theo inode (hard link): `acc.json` tương đối và
  `/…/acc.json` là cùng một file. Giới hạn đã biết: symlink trỏ tới một file
  **chưa tồn tại** không được giải, nên hai provider trỏ vào đó theo hai đường
  khác nhau chưa bị chặn — đừng cấu hình như vậy.
- 🔒 **Chỉ `.env` có giá trị** (gitignore, quyền 0600) cho: bí mật (API key,
  file credential — lộ tên tài khoản) và mọi dữ liệu của nhà cung cấp
  (endpoint, client ID, `CLIENT_VERSION`, `API_VERSION`, `BETA`, `USER_AGENT`,
  `MODEL_PREFIX`). `.env.example` chỉ ghi giá trị do dự án quyết — danh sách
  trắng `HOST`, `PORT`, `COOLDOWN`, `TIMEOUT`, `REFRESH_*`, `PROVIDERS`,
  `TYPE`, `ORIGINATOR`, `MAX_TOKENS`; test `TestEnvExampleProjectValuesOnly`
  đỏ nếu khoá nào khác (kể cả khoá thêm sau này) có giá trị trong mẫu.
- Test `TestEnvExampleComplete` đỏ nếu `.env.example` thiếu khoá code đòi,
  thừa khoá không ai đọc, hoặc có adapter không có provider mẫu.
- Biến env có sẵn (khác rỗng) thắng `.env`; đổi file bằng `-env <đường dẫn>`
  (file chỉ đích danh mà không có → lỗi). Host chỉ nhận loopback — code từ
  chối `0.0.0.0` / IP LAN.
- Còn trong code, có chủ ý: hình dạng giao thức (path API, tên header, tên
  field) và giới hạn an toàn (`internal/provider/sse.go`; trần body chat
  64 MiB ở `internal/server/server.go`; `ReadHeaderTimeout` ở `main.go`).

**Thêm gì thì sửa gì:**

| thêm | sửa |
|---|---|
| tài khoản thứ hai của kiểu đã có | **chỉ env**: thêm tên vào `SUBLLM_PROVIDERS` + 3 dòng `TYPE` / `MODEL_PREFIX` / `CRED_FILE` (ví dụ cuối `.env.example`) |
| ghi đè một giá trị cho riêng một tài khoản | **chỉ env**: `SUBLLM_PROVIDER_<N>_<K>` |
| backend đổi URL / client ID / version | **chỉ env**: `SUBLLM_TYPE_<T>_<K>` |
| giao thức mới (wire protocol khác) | **code**: package `internal/provider/<kiểu>/` cài `Provider`, thêm **một mục** vào map `adapters` ở `config.go`, thêm provider + khối `SUBLLM_TYPE_<KIỂU>_*` vào `.env.example` |

## Endpoint

- `GET /v1/models` — gộp catalog mọi provider (`owned_by` = tên provider).
- `POST /v1/chat/completions` — non-stream lẫn `stream: true` (SSE chunks
  chuẩn OpenAI). Field `provider` tuỳ chọn. Field `reasoning_effort` (tên
  chuẩn OpenAI) tuỳ chọn: kiểu `codex` chuyển nguyên thành
  `"reasoning": {"effort": …}` — mức hợp lệ tuỳ model, backend kiểm (sai → 400,
  không thử lại, không tính lỗi tài khoản); không gửi = mặc định của model.
  Kiểu `claude` chưa hỗ trợ → 400 rõ ràng (không lặng lẽ bỏ qua).
- Lỗi trước khi có chữ đầu tiên trả mã HTTP thật, kể cả khi `stream: true`:
  400 request sai (không dịch được, vd role lạ, upstream trả 400, hoặc báo
  `invalid_request_error` giữa stream — vd không tải được link ảnh), 404 không
  có provider, 413 body quá 64 MiB, 429 + `Retry-After` (luôn có, lấy từ
  upstream hoặc `SUBLLM_COOLDOWN`), 502 còn lại; lỗi từ backend có thông báo
  mở đầu bằng tên provider đã thử cuối. Client huỷ giữa chừng hay request
  không dịch được không bị tính là lỗi của tài khoản trong `/status`; 400 từ
  upstream cũng vậy (request sai, tài khoản nào cũng lỗi như nhau) — client
  nhận 400. Request được kiểm trước khi đụng credential, nên request không
  dịch được không kích refresh token.
- `GET /status` — số đếm từng tài khoản (xem trên).
- `GET /healthz`.
- Mọi route: request từ trình duyệt (header `Origin`, hoặc `Sec-Fetch-Site`
  khác `none`) hay `Host` không phải loopback → 403, để một trang web (kể cả
  qua DNS rebinding) không tiêu được gói khi chưa đặt `SUBLLM_API_KEY`.
- Có `SUBLLM_API_KEY` thì gửi `Authorization: Bearer <key>`.

## Gọi thử

```bash
# đặt mức suy nghĩ (reasoning effort)
curl http://127.0.0.1:18080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "<model>", "reasoning_effort": "<mức>", "messages": [{"role": "user", "content": "Xin chào"}]}'

# ảnh + file: content là mảng part chuẩn OpenAI, một lượt trộn được nhiều
# text / ảnh (data URI base64 hoặc URL) / file (data URI base64, vd PDF)
curl http://127.0.0.1:18080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "<model>", "messages": [{"role": "user", "content": [
        {"type": "text", "text": "Câu 1: ảnh có gì?"},
        {"type": "image_url", "image_url": {"url": "data:image/png;base64,<...>"}},
        {"type": "text", "text": "Câu 2: file này là hợp đồng số mấy?"},
        {"type": "file", "file": {"filename": "a.pdf", "file_data": "data:application/pdf;base64,<...>"}}]}]}'

# chọn provider (tài khoản) đích danh
curl http://127.0.0.1:18080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "<model>", "provider": "<tên provider>", "stream": true,
       "messages": [{"role": "user", "content": "Hi"}]}'

curl http://127.0.0.1:18080/status
```

`<model>` = tên model của gói, lấy từ `GET /v1/models`; tên phải mang tiền tố
`MODEL_PREFIX` của provider muốn route tới.

Python OpenAI SDK: `base_url="http://127.0.0.1:18080/v1"`, `api_key` = giá trị
`SUBLLM_API_KEY` (trống thì tuỳ ý).

## Cấu trúc

```
main.go                     flag -env + wiring + tắt êm
config.go                   NƠI DUY NHẤT nạp .env + đọc/kiểm mọi cấu hình, map adapters
internal/tokenstore/        file credential + refresh OAuth + đọc hạn JWT
internal/provider/          interface Provider, Registry, Router (failover + /status), lỗi có kiểu, đọc SSE
internal/provider/codex/    adapter kiểu codex: dịch request + gọi upstream
internal/provider/claude/   adapter kiểu claude
internal/server/            mặt HTTP OpenAI (/v1/models, /v1/chat/completions) + /healthz + /status
```

## Phát triển

```bash
gofmt -l .                 # phải rỗng
go vet ./...
go test -race ./...
```

CI (GitHub Actions, `.github/workflows/ci.yml`) chạy đúng ba bước này cho mỗi
push lên `main` và mỗi pull request. License: [MIT](LICENSE).
