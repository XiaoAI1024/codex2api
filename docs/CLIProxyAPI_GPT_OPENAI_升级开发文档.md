# codex2api 对齐 CLIProxyAPI（GPT/OpenAI）升级开发文档

## 1. 目标与范围

基于上游 `CLIProxyAPI` 最近 GPT/OpenAI 相关更新，规划并实施 `codex2api` 升级，目标是：

1. 模型清单治理（含 GPT-5.3/5.4/5.4-mini、过时条目治理）
2. OpenAI 图片接口兼容（`/v1/images/*`、`gpt-image-2`、`n` 参数兼容）
3. per-model thinking 支持（按模型控制 `reasoning_effort`）
4. 流式 late usage 补全（修复 `response.completed.usage` 晚到丢失）

不在本次范围：

- 前端复杂运营功能（多租户控制台、灰度面板）
- 非 GPT/OpenAI 相关协议重构

---

## 2. 上游参考变更（CLIProxyAPI）

### 2.1 关键提交

- `e935196d`：Codex 内建 `gpt-image-2`
- `a1881596` / `fd71960c`：OpenAI 图片 handler 移除不支持 `n`
- `a4c1e32f`：清理过时 GPT-5 模型条目
- `a824e7cd`：新增 GPT-5.3 / 5.4 / 5.4-mini + thinking levels
- `fee73693`：OpenAI-compat per-model thinking
- `65e9e892`：流式 late usage 补全（`response.completed.usage`）

### 2.2 可直接参考的代码位置

#### A. 模型注册表/治理

- `/Volumes/nvme/ai/CLIProxyAPI/internal/registry/models/models.json`
  - `codex-free/codex-team/codex-plus/codex-pro` 中的 GPT-5.3/5.4/mini 与 thinking levels
- `/Volumes/nvme/ai/CLIProxyAPI/internal/registry/model_definitions.go`
  - `WithCodexBuiltins`
  - `codexBuiltinImageModelInfo`
  - `GetCodex*Models`
- `/Volumes/nvme/ai/CLIProxyAPI/sdk/cliproxy/service.go`
  - `buildCodexConfigModels`

#### B. OpenAI 图片兼容

- `/Volumes/nvme/ai/CLIProxyAPI/internal/api/server.go`
  - `setupRoutes` 中 `/v1/images/generations`、`/v1/images/edits`
- `/Volumes/nvme/ai/CLIProxyAPI/sdk/api/handlers/handlers.go`
  - `getRequestDetails` 对 `gpt-image-2` 的端点约束
- `/Volumes/nvme/ai/CLIProxyAPI/sdk/api/handlers/openai/openai_images_handlers.go`
  - `ImagesGenerations`
  - `imagesEditsFromMultipart`
  - `imagesEditsFromJSON`
  - `n` 参数移除兼容

#### C. per-model thinking

- `/Volumes/nvme/ai/CLIProxyAPI/internal/config/config.go`
  - `OpenAICompatibilityModel.Thinking`
- `/Volumes/nvme/ai/CLIProxyAPI/internal/registry/model_registry.go`
  - `ThinkingSupport`
  - `cloneModelInfo` 深拷贝 thinking
- `/Volumes/nvme/ai/CLIProxyAPI/sdk/cliproxy/service.go`
  - `registerModelsForAuth` 中模型级 thinking 注入
- `/Volumes/nvme/ai/CLIProxyAPI/internal/runtime/executor/openai_compat_executor.go`
  - `Execute` / `ExecuteStream` / `CountTokens` 中统一 `thinking.ApplyThinking(...)`

#### D. 流式 late usage 补全

- `/Volumes/nvme/ai/CLIProxyAPI/internal/translator/openai/openai/responses/openai_openai-responses_response.go`
  - `oaiToResponsesState`（`CompletionPending` / `CompletedEmitted`）
  - `buildResponsesCompletedEvent(...)`
  - `ConvertOpenAIChatCompletionsResponseToOpenAIResponses(...)`
- `/Volumes/nvme/ai/CLIProxyAPI/internal/runtime/executor/openai_compat_executor.go`
  - 上游缺 `[DONE]` 时注入 synthetic `data: [DONE]`

---

## 3. codex2api 现状审计结果（蜂群）

### P0 缺口

1. **模型清单治理不足**
   - 当前 `SupportedModels` 为硬编码：
     - `/Volumes/nvme/ai/codex2api/proxy/handler.go`
   - 校验依赖静态列表：
     - `/Volumes/nvme/ai/codex2api/api/validation.go`
   - 管理端直接返回静态值：
     - `/Volumes/nvme/ai/codex2api/admin/handler.go` (`ListModels`)

2. **OpenAI 图片接口缺失**
   - 路由未注册 `/v1/images/*`：
     - `/Volumes/nvme/ai/codex2api/proxy/handler.go` (`RegisterRoutes`)
   - OpenAPI 未定义图片路径：
     - `/Volumes/nvme/ai/codex2api/api/openapi.yaml`
   - 仅有 message-content `image_url` 到 `input_image` 的转换：
     - `/Volumes/nvme/ai/codex2api/proxy/translator.go` (`convertMessagesToInput`)

3. **per-model thinking 未实现**
   - 全局提取/钳位，无模型能力差异：
     - `/Volumes/nvme/ai/codex2api/proxy/handler.go` (`extractReasoningEffort`)
     - `/Volumes/nvme/ai/codex2api/proxy/translator.go` (`clampReasoningEffort`)

4. **late usage 补全缺失**
   - 流结束判定偏早：
     - `/Volumes/nvme/ai/codex2api/proxy/handler.go` (`isTerminalUpstreamEvent`)
   - translator 见 `response.completed` 直接 done：
     - `/Volumes/nvme/ai/codex2api/proxy/translator.go` (`StreamTranslator.Translate`)
   - `stream_options` 被清理，`include_usage` 语义不可用：
     - `/Volumes/nvme/ai/codex2api/proxy/handler.go`
     - `/Volumes/nvme/ai/codex2api/proxy/translator.go`

### P1 缺口

- `system_settings` 无模型治理字段：
  - `/Volumes/nvme/ai/codex2api/database/postgres.go`
- 相关回归测试缺失（图片、per-model thinking、late usage）

---

## 4. 实施计划（按优先级）

## Phase 0：基线冻结（P0）

- [ ] 记录当前模型清单、`/v1/models` 输出快照
- [ ] 记录当前 `responses/chat` stream usage 统计样本
- [ ] 建立升级分支和回滚点

---

## Phase 1：模型目录治理（P0）

### 目标

- 从硬编码 `SupportedModels` 升级为“模型目录 + 能力元数据”。

### 代码改动

- 新增：`/Volumes/nvme/ai/codex2api/proxy/model_catalog.go`
  - `ModelCapability`（如 `AllowedReasoningEfforts`, `SupportsImage`, `MaxCompletionTokens`）
  - `GetModelCatalog()`
  - `IsModelAllowed(model string)`
  - `ListPublicModels()`
- 替换引用：
  - `/Volumes/nvme/ai/codex2api/proxy/handler.go`
    - `Responses`, `ChatCompletions`, `ListModels`
  - `/Volumes/nvme/ai/codex2api/admin/handler.go`
    - `ListModels`
  - `/Volumes/nvme/ai/codex2api/api/validation.go`
    - `ModelValidator(...)` 改为目录驱动

### 交付标准

- [ ] `gpt-5.3-codex`、`gpt-5.4`、`gpt-5.4-mini` 正常可见/可校验
- [ ] 过时模型条目已移除或标记禁用

---

## Phase 2：OpenAI 图片兼容（P0）

### 目标

- 支持 `/v1/images/generations`、`/v1/images/edits`，并兼容 `gpt-image-2`。

### 代码改动

- 新增：`/Volumes/nvme/ai/codex2api/proxy/images_handler.go`
  - `ImagesGenerations`
  - `ImagesEdits`
- 修改：`/Volumes/nvme/ai/codex2api/proxy/handler.go`
  - `RegisterRoutes` 添加图片路由
- 修改：`/Volumes/nvme/ai/codex2api/proxy/translator.go`
  - 扩展图片输入形态（`image_url`/base64/data URL）
- 修改：`/Volumes/nvme/ai/codex2api/api/openapi.yaml`
  - 增加 images API 规范

### 兼容策略

- [ ] 对不支持的 `n` 参数执行“忽略并记录 debug 日志”（与上游方向一致）
- [ ] 非图片端点请求 `gpt-image-2` 返回明确错误（4xx/503 策略需定）

---

## Phase 3：per-model thinking（P0）

### 目标

- `reasoning_effort` 由“全局钳位”升级为“按模型能力钳位/回退”。

### 代码改动

- 修改：`/Volumes/nvme/ai/codex2api/proxy/model_catalog.go`（新增能力字段）
- 修改：`/Volumes/nvme/ai/codex2api/proxy/translator.go`
  - `clampReasoningEffortByModel(model, effort)`
- 修改：`/Volumes/nvme/ai/codex2api/proxy/handler.go`
  - 在 `Responses` / `ChatCompletions` 中应用 model-aware 策略并记录日志

### 策略建议

- [ ] 模型不支持时剥离 reasoning 字段
- [ ] `xhigh` 超上限时降级到模型最大可用档位

---

## Phase 4：stream late usage 补全（P0）

### 目标

- `response.completed.usage` 晚到时不丢失，落库准确。

### 代码改动

- 修改：`/Volumes/nvme/ai/codex2api/proxy/translator.go`
  - 增加 `CompletionPending` / `CompletedEmitted` 状态
  - `response.completed` 延后至最终收口点发出
- 修改：`/Volumes/nvme/ai/codex2api/proxy/executor.go`（如需）
  - 上游缺 `[DONE]` 时注入 synthetic `[DONE]`
- 修改：`/Volumes/nvme/ai/codex2api/proxy/handler.go`
  - 引入终止后 usage 补采窗口
  - 按 `stream_options.include_usage` 控制语义（恢复支持）

---

## Phase 5：测试与验收（P0）

### 测试文件

- `/Volumes/nvme/ai/codex2api/proxy/handler_test.go`
- `/Volumes/nvme/ai/codex2api/proxy/translator_test.go`
- `/Volumes/nvme/ai/codex2api/proxy/handler_transport_test.go`
- `/Volumes/nvme/ai/codex2api/proxy/wsrelay/*_test.go`

### 用例矩阵

- [ ] 新模型路由 + 校验 + 列表展示
- [ ] 图片生成/编辑 + `n` 参数兼容
- [ ] 每模型 reasoning_effort 钳位（含降级）
- [ ] stream late usage（`finish_reason` 后 usage 到达）
- [ ] 无 `[DONE]` 收尾容错

---

## Phase 6：发布与回滚（P1）

- [ ] 增加开关：
  - `enable_model_catalog`
  - `enable_images_api`
  - `enable_per_model_reasoning`
  - `enable_stream_late_usage_patch`
- [ ] 先灰度后全量
- [ ] 失败回滚到 Phase 0 基线

---

## 5. 审计执行计划（蜂群）

> 本节用于“开发文档完成后开启蜂群审计”的执行说明。

### 审计任务分工

1. **架构审计（Explorer-A）**
   - 校验模型目录、图片路由、reasoning、stream usage 四条链路是否全部接入
2. **协议审计（Explorer-B）**
   - 对比 OpenAI 兼容行为（路径、字段、错误码、stream 事件）
3. **测试审计（Explorer-C）**
   - 检查用例覆盖和回归风险

### 审计输出要求

- 每项结论必须给出：
  - 代码路径（绝对路径）
  - 函数名
  - 结论（通过/风险/阻塞）
  - 建议修复

