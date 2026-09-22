# lobsterai2api

OpenAI-compatible API bridge for LobsterAI — multi-account pool with credit-based load balancing, SSE streaming, and automatic token refresh.

> **This repository is a fork of [xinxinshuhao-create/lobsterai2api](https://github.com/xinxinshuhao-create/lobsterai2api)** (MIT), based on upstream commit `fe299f1`.
> The original bridge, account pool, OAuth login and OpenAI-compatible API are the work of the original author.
> See [What's new in this fork](#whats-new-in-this-fork) for what was changed here.

- Language: Go (zero external dependencies, pure stdlib)
- Port: `:8367` (configurable via config or `LB2A_LISTEN`)

## Architecture

```
client (OpenAI SDK)
   │ POST /v1/chat/completions (Bearer ***)
   ▼
server: pool picks account (highest credits, healthy) → check token → forward
   ▼
upstream chat API (SSE only)
   ▼ on error → classify → cooldown/disable → rotate to next account (max 3)
```

## Build

```bash
go build -o lobsterai2api.exe ./cmd/server
go build -o login.exe ./cmd/login
go build -o credit.exe ./cmd/credit
go build -o checkin.exe ./cmd/checkin
```

## Login (add account)

```bash
./login.sh
# or manually:
./login.exe url   # prints login URL (local callback server ready)
# open URL in browser → phone/WeChat login
./login.exe poll  # wait for callback → exchange → save auths/lobsterai-<uid>.json
```

## Run

```bash
./lobsterai2api.exe -config config.json
```

## Credit query

```bash
./credit.sh        # human-readable
./credit.exe       # JSON output (for scripts)
```

## Daily checkin

The server scheduler already signs in at 09:00 and 21:00 local time (`schedule.checkin_hours`), then refreshes balances and unfreezes cooled accounts.

Standalone / cron (requires `LB2A_UPSTREAM_BASE`):

```bash
./checkin.sh
# 5 9,21 * * * cd /path/to/lobsterai2api && ./checkin.sh >> data/checkin.log 2>&1
```

Example line:

```
[2026-09-14 09:05:01] [10001] 签到成功 +100 分；剩余 832.35 分
```

## Test

```bash
# non-streaming
curl -s http://127.0.0.1:8367/v1/chat/completions \
  -H "Authorization: Bearer ***" \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"你好"}],"stream":false}'

# streaming
curl -s http://127.0.0.1:8367/v1/chat/completions \
  -H "Authorization: Bearer ***" \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"你好"}],"stream":true}'

# model list
curl -s http://127.0.0.1:8367/v1/models -H "Authorization: Bearer ***"

# status（账号池 + 定时签到/保活）
curl -s http://127.0.0.1:8367/status
# 看 schedule.summary / checkin_today / missed / last_checkin
# 进程关掉后看 data/schedule.json（最近 20 次，重启仍在）
```

## Configuration

See `config.example.json`. Environment variable prefix `LB2A_*`:

| Variable | Description |
|---|---|
| `LB2A_LISTEN` | Listen address |
| `LB2A_API_KEY` | Local auth key |
| `LB2A_AUTH_DIR` | Auth file directory |
| `LB2A_STATE_FILE` | Pool state file |
| `LB2A_HARD_CREDIT` / `LB2A_SOFT_RATE` | Cooldown durations |
| `LB2A_ERR_THRESHOLD` / `LB2A_ERR_COOLDOWN` | Error threshold and cooldown |
| `LB2A_TIMEOUT_SECONDS` | Upstream timeout |
| `LB2A_UPSTREAM_BASE` | Upstream API base URL (required) |
| `LB2A_LOGIN_PORTAL` | Login portal URL for OAuth flow (required for login) |
| `LB2A_UPDATE_API` | Official desktop version API (optional; used by daily checkin User-Agent) |

## Features

- **Multi-account pool** — auto-load auth files from `auths/`, pick highest-credit healthy account per request
- **OpenAI-compatible** — `/v1/chat/completions` (streaming + non-streaming), `/v1/models`, `/status`, `/healthz`
- **OAuth login** — local callback server, browser-based login, auto-save credentials
- **Token refresh** — JWT expiry parsing, proactive refresh 10min before expiry, session death auto-disable
- **Error classification** — hard credit cooldown 12h, 429 soft cooldown 60s, consecutive errors 3→10m, refresh rejected → disable
- **Request-level rotation** — up to 3 account switches per request
- **Scheduler** — daily checkin (09:00/21:00) + token keepalive (22:00); last/next fire, missed slots and per-account results on `GET /status` (`schedule`), persisted in `data/schedule.json`
- **Dynamic model list** — fetched from upstream API, cached 1h, falls back to static table

## What's new in this fork

Diff against upstream `fe299f1`:

- **Real daily checkin** — upstream shipped `DailyCheckin` as a no-op TODO; here it drives the desktop activity slot (`slot` → `context` → `actions/check_in`) with an idempotency key and a client version resolved from the official update channel.
- **Standalone `checkin` command** — `./checkin.sh` (or `go run ./cmd/checkin`) signs in every account in `auths/`, one line per account, suitable for cron.
- **Credit precision** — balances are read as `float64` (`CreditsRemaining`) instead of being truncated to `int64`.
- **Scheduler observability** — each checkin/keepalive run is recorded, persisted to `data/schedule.json` (last 20 runs, survives restarts) and exposed on `GET /status` under `schedule`: `summary`, `checkin_today`, `missed`, `next_checkin`, `last_checkin`, per-account results.
- **Windows helpers** — `start.cmd` / `login.cmd` build-and-run scripts for a one-click local setup.
- **Tests** — unit tests for the scheduler (status / persistence / summary) and for the checkin flow (`httptest`), run with `go test ./...`.

## Security & privacy

- `auths/` (account tokens), `data/` (pool state and run history) and `config.json` hold credentials and personal data; they are **git-ignored — never commit them**.
- Access/refresh tokens are stored in plaintext under `auths/` by design: keep the machine trusted, and leave the listener on localhost unless you set `LB2A_API_KEY` or put an authenticating reverse proxy in front.
- Nothing in this repo contains a real account, uid or token — examples use placeholder ids such as `10001`.

## Known limitations / TODO

- Dynamic model list from upstream API (cached 1h, falls back to static table)

## License

MIT — see [LICENSE](LICENSE), kept byte-identical to upstream so that GitHub detects the license. Original work © 2026 xinxinshuhao-create; fork modifications © 2026 greatleo31.
