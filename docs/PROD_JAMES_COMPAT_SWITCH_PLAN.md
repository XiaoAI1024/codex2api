# prod-james-compat 生产切换开发文档

## 背景

目标是在当前仓库 `XiaoAI1024/codex2api` 内生成一个可直接用于生产切换的兼容分支，而不是让生产实例直接依赖第三方远端 `james-6-23/codex2api`。

当前基础：

- 兼容分支：`prod-james-compat`
- 分支基线：`james/main`
- 基线提交：`163ea1d fix: align Docker builder Go version`
- 原生产分支关键提交：`e03a9e5 fix: make image generation tool explicit`

生产切换目标：

```bash
git fetch origin --prune
git checkout -B main origin/prod-james-compat
docker compose -f docker-compose.local.yml up -d --build
```

## 核心原则

1. 以 `james/main` 为主体，保留其 UI、模型注册、图片工作台、Anthropic 兼容、Redis TLS、日志等更新。
2. 补回当前生产依赖的兼容行为，避免切换后回归。
3. 所有兼容行为必须有回归测试；旧测试中断言旧行为的用例必须翻转，不能让“测试通过”掩盖回归。
4. 生产切换命令只依赖 `XiaoAI1024/codex2api`，不直接依赖 `james-6-23/codex2api`。
5. 生产 runbook 必须可证明备份有效；代码回滚和完整数据回滚必须分开写清楚。

## 必须保留的生产行为

### 1. Version header 策略

现状风险：

- `james/main` 的 `proxy/executor.go` 和 `proxy/wsrelay/executor.go` 会主动设置 `Version`。
- 当前生产曾因 `Version` 与模型能力不匹配导致 `gpt-5.5` 请求失败。

目标行为：

- 默认不主动合成 `Version`。
- 默认不透传下游 `Version`，因为无法确定下游版本是否适配当前上游。
- 仍保留 `User-Agent`、`X-Stainless-*` 设备画像能力，不影响请求身份稳定化。

涉及文件：

- `proxy/executor.go`
- `proxy/wsrelay/executor.go`
- `proxy/executor_test.go`
- `proxy/wsrelay/executor_test.go`

测试要求：

- HTTP 上游请求默认没有 `Version`。
- WebSocket 上游请求默认没有 `Version`。
- 即使下游 headers 带 `Version`，也不向上游透传。
- 现有旧断言必须翻转：
  - `proxy/executor_test.go` 中要求 `Version` 存在的断言改为要求为空。
  - `proxy/wsrelay/executor_test.go` 中要求 WebSocket `Version` 存在的断言改为要求为空。

### 2. 图片工具 explicit 模式

现状风险：

- 普通 `/v1/responses` 如果被自动补 `image_generation`，文本请求会误触发 `gpt-image-2`，出现图片限流。

目标行为：

- 普通 `/v1/responses`、`/responses`、WebSocket 请求不自动补 `image_generation`。
- 如果下游请求已经显式携带 `tools[].type=image_generation`，则保留并执行图片工具 bridge 指令。
- `/v1/images/generations`、`/v1/images/edits` 继续内部构造 `image_generation`，仍走 `gpt-image-2`。
- `model=gpt-image-2` 或 `gpt-image-2-*` 的普通 Responses 请求可以由 `normalizeResponsesImageOnlyModel` 规范化为图片工具请求，这是显式选择图片模型的行为。

涉及文件：

- `proxy/translator.go`
- `proxy/translator_test.go`
- `proxy/images.go`
- `proxy/images_test.go`
- `proxy/wsrelay/executor_test.go`

测试要求：

- 普通 `gpt-5.3/gpt-5.4/gpt-5.5` Responses 请求不新增 `image_generation`。
- 显式 `image_generation` 工具不会被删除，且 bridge instructions 只在显式工具存在时注入。
- `/v1/images/generations` 构造的 body 包含 `tool_choice.type=image_generation`。
- `/v1/images/edits` 构造的 body 包含 `tool_choice.type=image_generation`。
- WebSocket `prepareWebsocketBody` 对普通文本请求不新增 `image_generation`；显式工具保持不丢失。
- 现有旧断言必须翻转：
  - `proxy/translator_test.go` 中普通 Responses 自动注入图片工具的测试改为 explicit 模式测试。
  - `TestPrepareResponsesBody_InjectsImageToolWithinToolLimit` 改为“不自动注入”，或仅保留在显式图片模型场景。

### 3. 物理删除账号

现状风险：

- `james/main` 使用 `SoftDeleteAccount` / `BatchSoftDeleteAccounts`，会保留账号行。
- 当前生产之前明确要求物理删库，否则账号历史行变多后管理台和调度会变慢。

目标行为：

- 后台删除账号走物理删除。
- 自动清理 unauthorized / expired / full usage 等路径也走物理删除。
- 删除账号时同步清理与账号强关联的表，至少包括：
  - `usage_logs`
  - `public_account_settlements`（如果表存在）
- 不删除 `account_events`。该表用于账号 added/deleted 趋势和审计，清理会破坏历史统计。
- 删除后内存池、fast scheduler、refresh scheduler 均移除该账号；session affinity 可以通过 fallback 自愈，但如实现成本低应同步清理。

涉及文件：

- `database/postgres.go`
- `database/sqlite.go`
- `database/sqlite_test.go`
- `auth/store.go`
- `admin/handler.go`

实现要求：

- 保留函数名 `SoftDeleteAccount` 和 `BatchSoftDeleteAccounts`，但内部改为物理删除，降低调用面改动。
- 新增私有 helper，例如 `deleteAccountRelatedRows`，对可选表使用存在性检查，避免旧库无表时报错。
- `SetError(id, "deleted")` 必须纳入硬删闭环；否则 401 auto-clean 仍会留下账号行。
- `SoftDeleteAccount` / `BatchSoftDeleteAccounts` / `SetError(id, "deleted")` 应使用事务完成强关联数据清理和账号删除。
- 事件写入顺序必须考虑物理删除后无法再关联账号。当前 `account_events` 没有外键，允许删除后写入事件；实现时必须确认异步事件写入不依赖 `accounts` 行存在。
- 生产如已有大量 `status='deleted'` 或 `error_message='deleted'` 历史行，需要另行提供一次性 purge 命令；本次运行时逻辑只保证新删除走物理删除。

测试要求：

- SQLite 删除账号后 `accounts` 表查不到该 id。
- 与该账号关联的 `usage_logs` 被清理。
- 与该账号关联的 `account_events` 不被清理，确保趋势/审计仍可用。
- 批量删除能删除多个账号。
- `SetError(id, "deleted")` 应物理删除账号行。

### 4. compact 参数裁剪

现状：

- `james/main` 已有 `PrepareCompactResponsesBody`，会删除 `include`、`store`、`stream`。

目标行为：

- 保持 compact 入口不会向上游发送 `include`、`store`、`stream`、`parallel_tool_calls`。
- 真实 compact 路由包括：
  - `/v1/responses/compact`
  - `/responses/compact`
  - `/backend-api/codex/responses/compact`
  - `/backend-api/codex/responses/*subpath` 中转到 compact 的路径
- 保持 compaction item 转 developer message。

涉及文件：

- `proxy/translator.go`
- `proxy/translator_test.go`
- `proxy/handler.go`

测试要求：

- client 传入 `include` 时 compact body 不包含 `include`。
- client 传入 `store` 时 compact body 不包含 `store`。
- client 或预处理注入 `parallel_tool_calls` 时 compact body 不包含 `parallel_tool_calls`。
- plaintext compaction 不以 `type=compaction` 发上游。

### 5. docker-compose 生产兼容

现状风险：

- `james/main` 移除了 `CODEX_PLUS_PORT` 映射。
- 当前生产可能仍依赖该端口。

目标行为：

- 保留 `CODEX_PLUS_PORT` 宿主端口映射，避免切换后端口失效。
- 由于应用只监听 `CODEX_PORT`，Plus 端口必须映射到容器内 `CODEX_PORT`，即形如 `${CODEX_PLUS_PORT:-8081}:${CODEX_PORT:-8080}`，不能映射到容器内 `CODEX_PLUS_PORT`。
- 保留 `image-assets:/data` 卷，支持图片工作台。
- `.env.example` 保留 `CODEX_PORT`、`CODEX_PLUS_PORT`、`IMAGE_ASSET_DIR`、`LOG_DIR`、`REDIS_TLS` 等配置。

涉及文件：

- `docker-compose.local.yml`
- `.env.example`

测试要求：

- `docker compose -f docker-compose.local.yml config` 能通过。
- resolved compose 输出同时包含两个端口映射：默认 `8080 -> 8080`、`8081 -> 8080`，并包含 `image-assets:/data`。
- 上线验证 `CODEX_PORT` 与 `CODEX_PLUS_PORT` 两个宿主端口都能访问同一个服务健康接口。

## 开发任务拆分

### Task A: 文档与基线确认

- 创建本文件。
- 确认分支基于 `james/main@163ea1d`。
- 确认当前工作树没有未预期改动。
- 文档审计阻塞项必须吸收进本文件后才能开发。

验证命令：

```bash
git status --short
git rev-parse --short HEAD
git log -1 --pretty='%h %s'
```

### Task B: Version header 策略

步骤：

1. 先修改测试，覆盖 HTTP 和 WebSocket 默认不带 `Version`，且下游 `Version` 不透传。
2. 修改 `proxy/executor.go`，移除默认 `req.Header.Set("Version", version)` 行为。
3. 修改 `proxy/wsrelay/executor.go`，移除默认 `headers.Set("Version", ...)` 行为。
4. 跑针对性测试。

验证命令：

```bash
go test ./proxy ./proxy/wsrelay -run 'Version|Header|Websocket' -count=1
```

### Task C: 图片 explicit 行为

步骤：

1. 增加普通 Responses 不自动加 `image_generation` 的测试。
2. 增加显式 `image_generation` 保留并注入 bridge instructions 的测试。
3. 确认 `/v1/images/*` 构造路径仍带 `image_generation`。
4. 增加 WebSocket `prepareWebsocketBody` explicit 测试。
5. 如发现普通文本请求被自动加图片工具，修正 `proxy/translator.go`。

验证命令：

```bash
go test ./proxy ./proxy/wsrelay -run 'Image|ResponsesBody|ImageGeneration|Images|Websocket' -count=1
```

### Task D: 物理删除账号

步骤：

1. 修改 SQLite 测试：删除账号后 `accounts` 不存在该 id，`usage_logs` 清理，`account_events` 保留。
2. 修改 `database/sqlite.go` 的 `SoftDeleteAccount` / `BatchSoftDeleteAccounts` 为物理删除。
3. 修改 `database/postgres.go` 的对应函数为物理删除。
4. 修改 `SetError(id, "deleted")` 路径为物理删除。
5. 核对 `auth/store.go` 自动清理路径调用仍正常。
6. 跑 database/auth/admin 相关测试。

验证命令：

```bash
go test ./database ./auth ./admin -run 'Delete|SoftDelete|Clean|Account' -count=1
```

### Task E: compact 与 compose 回归

步骤：

1. 确认 compact 参数裁剪测试存在，补充 `parallel_tool_calls` 裁剪测试。
2. 修改 `docker-compose.local.yml` 保留 `CODEX_PLUS_PORT`，并映射到容器内 `CODEX_PORT`。
3. 跑 compose config。开发环境没有 `.env` 时，可先复制 `.env.example` 到临时 `.env` 或使用 `env $(grep -v '^#' .env.example | xargs) docker compose ... config` 进行只读验证。
4. 检查 resolved compose 中 `codex2api` 服务有两个宿主端口：
   - `${CODEX_PORT:-8080}` -> 容器内 `${CODEX_PORT:-8080}`
   - `${CODEX_PLUS_PORT:-8081}` -> 容器内 `${CODEX_PORT:-8080}`

验证命令：

```bash
go test ./proxy -run 'Compact|Compaction' -count=1
docker compose -f docker-compose.local.yml config >/tmp/codex2api-compose-config.txt
```

### Task F: 全量验证与推送

验证命令：

```bash
go test ./... -count=1
go test -race ./auth ./cache ./proxy ./proxy/wsrelay -count=1
docker compose -f docker-compose.local.yml config >/tmp/codex2api-compose-config.txt
git diff --check
```

推送命令：

```bash
git push -u origin prod-james-compat
```

## 生产切换命令

### 切换前备份与 preflight

```bash
set -euo pipefail

cd /opt/1panel/apps/codex2api

TS=$(date +%Y%m%d-%H%M%S)
BACKUP_DIR=/opt/backups/codex2api-switch-$TS
umask 077
mkdir -p "$BACKUP_DIR"
chmod 700 "$BACKUP_DIR"

if ! git diff --quiet || ! git diff --cached --quiet; then
  git status --short
  echo "工作树存在未提交的 tracked 改动，停止切换"
  exit 1
fi
git branch backup-before-james-$TS
cp .env docker-compose.local.yml "$BACKUP_DIR"/

docker compose -f docker-compose.local.yml config > "$BACKUP_DIR/compose.resolved.before.yml"

df -h . "$BACKUP_DIR"

docker compose -f docker-compose.local.yml exec -T postgres \
  sh -lc 'pg_dump -U "$POSTGRES_USER" "$POSTGRES_DB"' \
  > "$BACKUP_DIR/postgres.sql"
test -s "$BACKUP_DIR/postgres.sql"

docker compose -f docker-compose.local.yml exec -T redis sh -euc '
redis-cli BGSAVE
while [ "$(redis-cli INFO persistence | awk -F: "/rdb_bgsave_in_progress/{gsub(/\r/,\"\",\$2); print \$2}")" = "1" ]; do
  sleep 1
done
test "$(redis-cli INFO persistence | awk -F: "/rdb_last_bgsave_status/{gsub(/\r/,\"\",\$2); print \$2}")" = "ok"
redis-cli LASTSAVE
' > "$BACKUP_DIR/redis-lastsave.txt"

docker compose -f docker-compose.local.yml down

docker volume inspect codex2api-local_image-assets >/dev/null 2>&1 || docker volume create codex2api-local_image-assets
docker volume inspect codex2api-local_pgdata codex2api-local_redisdata codex2api-local_image-assets > "$BACKUP_DIR/volumes.inspect.json"
docker run --rm \
  -v codex2api-local_pgdata:/pgdata:ro \
  -v codex2api-local_redisdata:/redisdata:ro \
  -v codex2api-local_image-assets:/image-assets:ro \
  -v "$BACKUP_DIR":/backup \
  alpine sh -lc 'du -sh /pgdata /redisdata /image-assets > /backup/volumes.du.txt; find /pgdata /redisdata /image-assets -maxdepth 2 | wc -l > /backup/volumes.file_count.txt'
docker run --rm \
  -v codex2api-local_pgdata:/pgdata:ro \
  -v codex2api-local_redisdata:/redisdata:ro \
  -v codex2api-local_image-assets:/image-assets:ro \
  -v "$BACKUP_DIR":/backup \
  alpine sh -lc 'tar czf /backup/volumes.tgz /pgdata /redisdata /image-assets'
test -s "$BACKUP_DIR/volumes.tgz"
```

### 切换

```bash
set -euo pipefail

cd /opt/1panel/apps/codex2api

if ! git diff --quiet || ! git diff --cached --quiet; then
  git status --short
  echo "工作树存在未提交的 tracked 改动，停止切换"
  exit 1
fi

git remote set-url origin https://github.com/XiaoAI1024/codex2api.git
git fetch origin --prune
git ls-remote --exit-code --heads origin prod-james-compat >/dev/null
git checkout -B main origin/prod-james-compat

grep -q '^IMAGE_ASSET_DIR=' .env || echo 'IMAGE_ASSET_DIR=/data/images' >> .env
grep -q '^CODEX_PLUS_PORT=' .env || echo 'CODEX_PLUS_PORT=8081' >> .env
grep -q '^LOG_DIR=' .env || echo 'LOG_DIR=logs' >> .env
grep -q '^LOG_DISABLED=' .env || echo 'LOG_DISABLED=false' >> .env
grep -q '^REDIS_USERNAME=' .env || echo 'REDIS_USERNAME=' >> .env
grep -q '^REDIS_TLS=' .env || echo 'REDIS_TLS=false' >> .env
grep -q '^REDIS_INSECURE_SKIP_VERIFY=' .env || echo 'REDIS_INSECURE_SKIP_VERIFY=false' >> .env

git rev-parse --short HEAD
docker compose -f docker-compose.local.yml config --quiet
docker compose -f docker-compose.local.yml up -d --build
docker compose -f docker-compose.local.yml logs --tail=200 codex2api
```

### 上线验证

```bash
set -euo pipefail
test -f .env
set -a
. ./.env
set +a

curl -fS http://127.0.0.1:${CODEX_PORT:-8080}/health
curl -fS http://127.0.0.1:${CODEX_PLUS_PORT:-8081}/health

curl -fS http://127.0.0.1:${CODEX_PORT:-8080}/v1/responses \
  -H "Authorization: Bearer <API_KEY>" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-5.3","input":"hi","stream":false}'

curl -fS http://127.0.0.1:${CODEX_PORT:-8080}/v1/images/generations \
  -H "Authorization: Bearer <API_KEY>" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-image-2","prompt":"一只戴墨镜的橘猫，赛博朋克风","size":"1024x1024","response_format":"b64_json"}'
```

### 仅代码回滚

适用于新镜像启动失败且数据库未发生不可逆变更的情况。

```bash
set -euo pipefail

cd /opt/1panel/apps/codex2api

TS=20260428-120000  # 替换为实际备份时间戳
BACKUP_DIR=/opt/backups/codex2api-switch-$TS

docker compose -f docker-compose.local.yml down
git checkout -B main "backup-before-james-$TS"
cp "$BACKUP_DIR/.env" .env
cp "$BACKUP_DIR/docker-compose.local.yml" docker-compose.local.yml
docker compose -f docker-compose.local.yml up -d --build
```

### 完整数据回滚

适用于新版本已执行数据库迁移或写入新数据，需要恢复到切换前状态的情况。执行前把 `TS` 改为目标备份目录后缀。

```bash
set -euo pipefail

cd /opt/1panel/apps/codex2api

TS=20260428-120000  # 替换为实际备份时间戳
BACKUP_DIR=/opt/backups/codex2api-switch-$TS

docker compose -f docker-compose.local.yml down -v

docker volume create codex2api-local_pgdata
docker volume create codex2api-local_redisdata
docker volume create codex2api-local_image-assets

docker run --rm \
  -v codex2api-local_pgdata:/pgdata \
  -v codex2api-local_redisdata:/redisdata \
  -v codex2api-local_image-assets:/image-assets \
  -v "$BACKUP_DIR":/backup \
  alpine sh -lc 'tar xzf /backup/volumes.tgz -C /'

git checkout -B main "backup-before-james-$TS"
cp "$BACKUP_DIR/.env" .env
cp "$BACKUP_DIR/docker-compose.local.yml" docker-compose.local.yml
docker compose -f docker-compose.local.yml up -d --build
```
