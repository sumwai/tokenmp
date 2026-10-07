# 协议兼容性与参数处理

本文件是 `docs/openapi.yaml` 的行为补充，描述机器可读规范表达不了的内容：四个方言之间
的参数取舍、跨协议降级时客户端能观察到的差异、流式用量帧的两种行为，以及用量分量与计费的关系。

规范文件给结构与状态码，本文件给取舍与原因。两者都只描述当前实现已经存在的行为；
实现变则同步改本文件（见 [CONTRIBUTING.md](../CONTRIBUTING.md) 的「行为变更与规范同步」）。

四个方言的转发端点：

| 端点 | 方言 | 适配器 |
|---|---|---|
| `POST /v1/chat/completions` | OpenAI Chat Completions | `internal/adapters/openaichat` |
| `POST /v1/responses` | OpenAI Responses | `internal/adapters/openairesponses` |
| `POST /v1/messages` | Anthropic Messages | `internal/adapters/anthropic` |
| `POST /v1beta/models/{model}:generateContent` | Gemini generateContent | `internal/adapters/gemini` |
| `POST /v1beta/models/{model}:streamGenerateContent` | Gemini generateContent | `internal/adapters/gemini` |

## 账户自助查询端点

`GET /v1/me/account` 让接入方自助读取自身权益，不需要运维执行 admin CLI：

- **鉴权**：与转发端点共用同一鉴权中间件，因此 401 口径完全一致（头格式非法、密钥无效或
  已过期、账户停用三类失败一律回同一种 401）。鉴权通过后的额度预检与窗口限额判定同样生效，
  账户无可用额度或已超限时本端点也会回 402 / 429 —— 转发端点与本端点是同一条鉴权链。
- **只读**：不改表、不写流水，每次请求直查数据库，不做缓存。后续若加缓存，失效时刻取
  「最短的包到期时间」与「限额重置时间」中较早的一个。
- **响应字段**：账户 `id` / `code`、可用包存量（未过期且 `remaining > 0`，按 `unit` 归拢）、
  生效中的窗口限额（已用量与重置时间）、最近流水摘要（模型、时间、`charged_amount`）。
- **不含敏感字段**：不返回密钥、上游凭据、商家与渠道内部标识。
- **最近流水**：`recent` 查询参数控制条数，缺省 10、上限 100、`0` 表示不返回；
  非 0 到 100 之间的整数回 400。`charged_amount` 是 `gross_amount × multiplier`，
  即本次应扣量；跨单位折算的部分由流水明细记载，不在摘要里展开。
- **窗口口径**：已用量与重置时间复用数据面判定所用的同一份窗口计算与聚合查询，
  两处不会漂移。`resets_at` 为 `null` 表示总量限额（`total`）不会自然重置。

## 两条请求定稿路径

选路结果里的渠道方言与客户端方言决定走哪条路径：

- **同协议透传**：渠道方言与客户端方言一致。网关在客户端原始报文上做字段级改写，
  未建模的字段逐字节保留。
- **跨协议重建**：渠道方言与客户端方言不一致。网关把请求解码为内部统一格式，再按上游方言重建；
  只有内部格式建模过的字段会出现在上游请求里。

跨协议重建是降级，不是等价转换：同协议透传保真度更高，因此同协议候选始终排在跨协议候选之前。

## 中间件可改写范围

本机可配置网关中间件（`TOKENMP_PLUGIN_FILES`），在四个时机介入转发。可改写范围与失败语义
都按「不因插件改变转发可用性」设计：任何插件异常都退回原样转发。

| 钩子 | 时机 | 可改写对象 | 要点 |
|---|---|---|---|
| `onRequest(body, ctx)` | 选路之前 | 请求体 | 改写后的请求体经客户端适配器重新解码，`model` 参与选路 |
| `onEvent(event, ctx)` | 流式分片写回客户端之前 | 内容事件 | 只处理 `events` 白名单声明的内容分片，返回 `null` 丢弃 |
| `onStreamEnd(ctx)` | 流式响应的终止帧之前 | 补发的分片 | 返回分片数组，用于冲刷跨分片缓冲的尾巴 |
| `onResponse(body, ctx)` | 非流式响应体写回客户端之前 | 响应体 | 按配置顺序依次改写 |

- **请求改写**：`onRequest` 的返回值替换整个请求体。协议、请求 id 与流式形态取自端点事实，
  不因改写而变；改写体未给出 `model` 时保留原值（路径携带模型名的方言即属这种情形）。
  返回值不是 JSON 对象、或改写后的请求体无法解码时，本次改写被丢弃，请求按原样继续。- **流式事件**：`onEvent` 只接受内容分片 `text_delta` / `tool_call_delta` / `reasoning_delta`，
  且事件名必须在模块导出的 `events` 白名单里。返回 `null` 丢弃该分片，返回对象表示改写，
  返回 `undefined` 表示不改写。用量、结束原因与流结束**不经**逐事件钩子改写，也不经过它；
  一次调用只能回一个分片。
- **流末补发**：`onStreamEnd(ctx)` 返回待写出的分片数组，写出时机在终止帧之前 —— 多数客户端
  见到结束原因后不再处理后续内容分片，排在终止帧之后等于白写。它存在的理由是逐事件钩子的
  两个限制：拿不到流结束分片、一次只能回一个分片；跨分片缓冲的改写（例如把 `<think>` 标签
  包裹的推理拆到另一个字段）总会有尾巴需要冲刷，而一帧可能同时含“推理尾”与“正文头”。
  未导出该钩子的中间件零开销（消费方按可选接口取用）。
- **推理透传**：OpenAI 方言的编码方向**会**下发推理 —— 流式写 `delta.reasoning_content`，
  非流式写 `message.reasoning_content`。该字段不属官方规范，但已是兼容渠道的既成事实，
  且本网关的解码方向本来就认它。过去编码方向不下发，于是同一份上游数据在「同协议透传」
  与「跨协议重建」下得到两种结果（前者原样转发、后者丢弃）；现在两条路一致。
  不认这个字段的客户端在推理阶段收不到正文，看上去像卡顿 —— 那不是延迟，是内容换了字段。
- **作用域**：模块可导出 `scope` 声明它只对哪些**名字**生效，由宿主在 Go 侧判定，
  **越界不进入 JS 运行时**：

  ```js
  export const scope = { models: ["MiniMax-M3"], vendors: ["minimax"] };
  ```

  | 轴 | 取值 | 选路前可判 |
  |---|---|---|
  | `models` | 客户端模型名（`upstream_model_map.model`）| 是 |
  | `protocols` | 客户端方言，如 `openai_chat` | 是 |
  | `vendors` | 渠道厂商标签（`upstream_channel.vendor`）| 否 |

  多轴取**与**，同轴取**或**；轴缺失或为空表示不限制。`onRequest` 在选路之前，
  厂商轴在它上面不参与判定（不因此跳过）；其余三个钩子跑在选路之后，三轴均参与。
  作用域只吃名字、不吃渠道主键：主键是环境事实（重建库就换号），同一份插件在不同库里会
  作用到不同渠道，文件里也看不出数字是谁。声明写坏按「不限制」处理并记警告；声明了、
  但在当前配置下一个都匹配不上的取值在 serve 启动时告警。
- **拒绝**：`onRequest` 里调用 `ctx.reject(status, message)` 会让本次请求以该状态码终止。
  错误体按请求方言编码，与已注册路径上其它错误同口径；错误码按状态码分类（401
  `unauthorized`、403 `forbidden`、429 `rate_limited`、404 `not_found`、其余 4xx
  `invalid_request`、其余 `internal`）。状态码不在 100–599 区间时取 403。
- **失败静默放行**：钩子抛错、超时（`onRequest` 与 `onResponse` 单次 250ms、`onEvent`
  单事件 50ms、`onStreamEnd` 单次 50ms、模块求值 1s）或返回非法值时，记一条结构化日志并按
  原样转发，不向客户端报错。中间件文件指纹变化时惰性重编译，重编译失败则保留上一份产物继续服务。
- **钩子上下文**：`ctx` 固定提供 `protocol`、`model`、`path`、`options`（模块导出的配置）、
  `state`（单请求共享对象）与 `agent`（调用方归属，通过鉴权的请求才有）。逐请求状态放在
  `ctx.state`；模块顶层状态在请求之间保留，不适合承载请求数据。
- **沙箱边界**：中间件由纯 Go 的 JavaScript 引擎在进程内执行；不注入 `fetch`、
  `XMLHttpRequest`、定时器与任何 Node API；模块导入限于插件目录内的相对路径；`eval` 与
  `Function` 构造器按源码字节数限长；`console` 输出进结构化日志并按条数限频。插件仍能
  读到完整的请求体与响应体（含用户消息与模型回答），加载一个中间件等同于让该中间件的
  代码接触这些内容，因此只应加载本机信任的中间件。
- **与 OpenAPI 的对应**：四个转发端点的请求体与响应体都可能被中间件改写；`/healthz` 与
  `/v1/me/account` 不装配中间件。

## 四方言参数处理清单

### 同协议透传

改写只作用于以下字段，其余字段保持原文（包括嵌套结构、键顺序之外的空白差异不保证：
发生改写时请求体被重新序列化）：

| 改写项 | 触发条件 | 取值 |
|---|---|---|
| `model` | 渠道配置了上游模型名 | 替换为上游模型名；字段缺失时补齐 |
| 输出上限 | 渠道配置了输出上限 | 客户端未声明则补齐，已声明且超限则钳制 |
| `stream_options.include_usage` | 仅 OpenAI Chat Completions、且请求为流式 | 合并为 `true`；已有其它 `stream_options` 键时逐键保留 |
| 渠道 × 模型覆盖项 | 渠道配置了 `request_overrides` | 按顶层键整体覆盖；覆盖项最后合并 |

`stream_options.include_usage` 的注入与客户端是否索取无关：网关需要用量帧来计费，
不注入就拿不到末尾的用量。是否把该帧回给客户端另见下文「流式用量帧」。

覆盖项的合并语义是**顶层键覆盖**，不做递归合并：递归会把「数组该替换还是追加」这类语义
落到协议无关的通用层，各协议无法各自表达；顶层覆盖已能覆盖 `temperature`、`top_p` 一类调参需求。
覆盖项不是 JSON 对象时在选路边界被丢弃并记日志，不中断转发。

> 输出上限改写字段（渠道 × 模型的有效输出上限）在当前装配下不由选路结果填充，
> 因此线上不产生该改写；上表保留它是因为适配器与内部格式已经建模该字段。

Gemini 的模型名与流式形态不在请求体里，而在 URL 路径上（`/v1beta/models/{model}:generateContent`
与 `:streamGenerateContent?alt=sse`），因此它的同协议透传改写有两处不同：

- `model`：模型名替换发生在发往上游的路径上，请求体里没有任何 `model` 字段；
- 输出上限：字段是嵌套的 `generationConfig.maxOutputTokens`，口径仍与其它方言相同。

### 跨协议重建

按内部统一格式重建上游请求体。以下字段在四方言之间映射：

| 内部字段 | OpenAI Chat Completions | OpenAI Responses | Anthropic Messages | Gemini generateContent |
|---|---|---|---|---|
| `model` | `model` | `model` | `model` | URL 路径的 `{model}` 段（请求体无此字段） |
| messages | `messages` | `input` | `messages` + 顶层 `system` | `contents` + 顶层 `systemInstruction` |
| system 消息 | `system` 角色消息 | `instructions` | 顶层 `system` | 顶层 `systemInstruction` |
| 工具结果 | 独立 `tool` 消息 | `function_call_output` 条目 | user 消息里的 `tool_result` 块 | user 消息里的 `functionResponse` 片段 |
| 工具调用 | assistant 消息的 `tool_calls` | `function_call` 条目 | assistant 消息的 `tool_use` 块 | model 消息的 `functionCall` 片段 |
| 图片 | `image_url` 内容片段 | `input_image` 内容片段 | `image` 块（`url` 或 base64 `source`） | `inlineData`（base64）或 `fileData`（URI） |
| 工具定义 | `tools[].function` | `tools`（扁平或嵌套） | `tools[].input_schema` | `tools[].functionDeclarations` |
| 工具选择 | 字符串或 `{"type":"function",...}` | 字符串或 `{"type":"function","name":...}` | `{"type":...}`；`required` 映射为 `any` | `functionCallingConfig.mode`；指定工具用 `ANY` + `allowedFunctionNames` |
| 输出上限 | `max_tokens` / `max_completion_tokens` | `max_output_tokens` | `max_tokens` | `generationConfig.maxOutputTokens` |
| `temperature` | 原值 | 原值 | 原值 | `generationConfig.temperature` |
| `stream` | 原值 | 原值 | 原值 | 无此字段，由路径后缀表达 |

重建过程中对消息顺序做归一化：

- 相邻同角色消息合并（Chat 的用户与助手消息、Anthropic 的角色交替要求）。
- 工具结果在 Chat 与 Responses 中展开为独立条目，在 Anthropic 中折叠进 user 消息并把
  `tool_result` 块前移，使上游能对齐它们应答的调用。
- 工具结果找不到对应的前置工具调用时回 `invalid_request`。

跨协议重建会丢失内部格式未建模的客户端字段。以 OpenAI Chat Completions 为例，这些字段
在**同协议透传**下保留、在**跨协议重建**下丢弃：`top_p`、`n`、`stop`、`seed`、
`presence_penalty`、`frequency_penalty`、`logit_bias`、`logprobs`、`top_logprobs`、
`response_format`、`parallel_tool_calls`、`user`。Responses 与 Anthropic 的协议专有字段
（`include`、`reasoning`、`truncation`、`previous_response_id`、`top_k`、`stop_sequences`、
`metadata`、`thinking` 等）同理。

推理内容也在丢弃之列：响应方向的 `reasoning_content` / `reasoning` 条目 / `thinking` 块
会被解码，但**不会重新编码**给客户端。四方言对可回传形态的要求不同
（Anthropic 的 thinking 块要求签名、Responses 的 reasoning 条目要求上下文标识），
从别的协议取到的纯文本无法重建。

### OpenAI Chat Completions 专有项

- `max_tokens` 与 `max_completion_tokens` 同时给出时以 `max_tokens` 为准，网关不额外发拒绝。
- `role: developer` 归一化为内部的 system。
- `stream_options.include_usage` 不是布尔时按「未索取」处理，不因一个可选开关把整条请求判为失败。
- 未识别的 `role`、内容片段类型、工具类型跳过；跳过全部消息时回 `invalid_request`。
- 未识别的 `tool_choice` 字符串或对象形态回 `invalid_request`，不静默丢弃客户端的工具选择要求。

### OpenAI Responses 专有项

- `input` 只接受字符串或条目数组；未识别的条目类型、角色、内容类型跳过。
- `instructions` 归一化为 system 消息，排在 input 之前。
- assistant 条目 content 为空时跳过该消息：工具循环回放时客户端会在 `function_call` 之前
  发一条空内容的 assistant message，上游接受它，网关不因此判为客户端错误。
- 未识别的 `tool_choice` 对象类型回 `invalid_request`。

### Anthropic Messages 专有项

- `model`、`max_tokens`、`messages` 必填；`max_tokens` 必须为正整数。
- `temperature` 区间是 `[0, 1]`，与 OpenAI 系方言的 `[0, 2]` 不同；超区间回 `invalid_request`。
- `messages` 只接受 `user` 与 `assistant`；工具结果放在 user 消息的 `tool_result` 块里。
- 请求体没有 `request_id` 字段，请求 id 由网关生成或取自客户端透传的 `X-Request-Id`。
- 请求方向的 `thinking` 与 `redacted_thinking` 块跳过：它们必须连同签名原样回传上游，
  建模反而会让跨协议重建产出缺签名的请求。
- 未识别的消息角色、内容块类型、图片 `source` 类型跳过。

### Gemini generateContent 专有项

- `contents` 必填；模型名与流式形态由 URL 路径给出，请求体没有 `model` 与 `stream` 字段。
  两者由适配器在端点边界解析，入口层回填到内部请求。
- 角色只有 `user` 与 `model`：`model` 归一化为内部 `assistant`，角色缺失按 `user` 处理。
- 顶层 `systemInstruction` 归一化为内部 system 消息；它只承载文本。
- 工具定义在 `tools[].functionDeclarations`；工具选择在 `toolConfig.functionCallingConfig.mode`，
  `AUTO` / `NONE` 原样映射，`ANY` 映射为内部「必须调用」，`ANY` 且只允许一个函数名时映射为指定工具。
  未识别模式回 `invalid_request`。
- 工具调用与结果按函数名关联：`functionCall` 不带 id，适配器合成内部 id，
  并用同名工具调用把它与后续的 `functionResponse` 对上。
- 请求方向的 `thought` 片段与未识别片段类型跳过；`generationConfig` 只建模
  `maxOutputTokens` 与 `temperature`，其余键忽略。

### 请求 id

优先取客户端透传的 `X-Request-Id`，其次请求体自带的 `request_id`（Chat 与 Responses 支持），
最后网关生成 128 位随机标识符。该值只作日志关联，不参与鉴权与选路，因此原样采信。

## 模型名替换

- 客户端请求的模型名是对外别名，也是选路键：网关按它查候选渠道与渠道倍率。
- 渠道配置了上游模型名时，该名字替换（或补齐）上游请求里的 `model`；
  Gemini 没有请求体模型字段，替换发生在发往上游的 URL 路径上。
- 响应体里的 `model` 由上游回显，可能与客户端请求的别名不同。
- `x-tokenmp-routed-model` 响应标头给出网关本次实际履约的模型名；渠道未配置上游模型名时
  不出现该标头。已向客户端写出字节后该标头不再改动。
- 用量流水记录两个模型名：履约模型（定价按它解析）与请求模型（查渠道倍率）。

## 跨协议降级

候选按两级拼接：

1. **同协议段**：渠道方言与客户端方言一致的候选。段内按优先级组加权随机定首选，
   同优先级内其余候选按渠道 id 顺序留在后面供回退。
2. **跨协议段**：不限方言查询补上的候选，去掉已在同协议段出现过的渠道
   （不限方言查询必然含同协议行），并只保留两侧方言都能经内部格式重建的候选。

同协议候选始终排在跨协议候选之前，低优先级的同协议候选也先于高优先级的跨协议候选。
两级都无候选时回 404（`model_not_found`）。

尝试预算按段计量，默认值：

| 段 | 默认尝试上限 |
|---|---|
| 同协议段 | 2 |
| 跨协议段 | 1 |
| 合计 | 4（防御性上限） |

这两个默认值没有对应的环境变量，只在装配层可覆盖。

同协议候选全部失败不会挤掉跨协议降级的机会。候选段耗尽仍失败时回最后一次尝试的错误。

客户端可观察的差异：

| 场景 | 客户端观察 |
|---|---|
| 同协议非流式 | 响应体是上游原始字节，逐字节一致 |
| 同协议流式 | 上游原始 SSE 帧逐帧透传（客户端未索取用量时抑制只承载用量的帧） |
| 跨协议非流式 | 响应体按客户端方言重新编码，不是上游原始字节 |
| 跨协议流式 | 按客户端方言重建帧序列 |
| 渠道配置了上游模型名 | 响应体 `model` 可能不同；`x-tokenmp-routed-model` 给出履约模型 |
| 跨协议重建 | 上游请求里未建模字段缺失（见上文清单） |

上游请求失败时换下一条候选重试，重试对客户端不可见（只要还没有写出字节）。
一旦已向客户端写出字节，就不再换渠道。

## 流式（SSE）行为

流式响应固定带三项响应头：

```
Content-Type: text/event-stream
Cache-Control: no-cache, no-transform
X-Accel-Buffering: no
```

每次写出后立即 Flush，避免帧被 HTTP 缓冲吞掉；每次写出前设置 10 秒写超时，
客户端超过该时长不读取时写操作失败、连接被切断，并取消上游调用。已写出状态码后不再改状态码。

超时口径：

- **不设整体超时**。长生成的整段耗时不可预估，用整段截止时间会误杀正常长流。
- 等待上游第一个字节：默认 30 秒（`TOKENMP_UPSTREAM_STREAM_FIRST_BYTE_TIMEOUT`）。
- 两帧之间的最大间隔：默认 60 秒（`TOKENMP_UPSTREAM_STREAM_IDLE_TIMEOUT`）。
  注释行与纯空块心跳不产生帧，但字节到达即视为有进展并重置计时。

### 用量帧的两种行为

只有 OpenAI Chat Completions 有「索取用量」的开关，另外三个方言的用量是协议固有事件。

| 方言 | 用量出现位置 | 客户端索取与否的影响 |
|---|---|---|
| OpenAI Chat Completions | 末尾一帧 `choices` 为空、`usage` 非空的 chunk | **有影响**。索取（`stream_options.include_usage: true`）时下发；未索取时不下发 |
| OpenAI Responses | 终止事件内嵌的 `response.usage` | 无影响，始终下发 |
| Anthropic Messages | `message_start.usage`（输入侧）与 `message_delta.usage`（累计值） | 无影响，始终下发 |
| Gemini generateContent | 末尾帧的 `usageMetadata`（每帧可能携带累计值） | 无影响，始终下发 |

Chat 的机制：网关为计费在**上游请求**里注入 `include_usage: true`。上游因此多发的那一帧，
只在客户端**也**索取过时才写回客户端；客户端未索取时网关丢弃该帧的原始字节，用量照常记账。
该抑制只作用于同协议透传路径；跨协议重建路径交给客户端方言的固有事件表达用量。

判定依据是解码出的分片类型（只承载用量的分片），不是字节前缀：无法解析或形状非预期的帧
原样转发，「宁多勿丢」。

### 断连语义

| 情形 | 客户端观察 |
|---|---|
| 上游在结束标记前断开 | 若已写出字节：状态码与响应头已是 200，Anthropic 与 Responses 下发 `error` 事件，Chat 与 Gemini 直接关闭连接（这两个方言没有流内错误帧）。若尚未写出字节：回普通错误响应，错误码 `upstream_unavailable`（502） |
| 上游空闲超时或首字节超时 | 同上；错误码 `upstream_timeout`（504） |
| 客户端断开 | 取消上游调用，不再换渠道重试；已经收到的用量仍写入流水 |
| 帧超过读取上限 | 错误码 `upstream_unavailable`（502） |

被截断的流只要在断开前收到过用量帧，该用量仍会落库。

## 用量 metric 与计费

### 分量映射

内部统一用量按白名单映射为计费分量，只写非零项；再在**落库边界**补一个 `request` 分量。

| metric | 来源字段 | 口径 |
|---|---|---|
| `input_token` | `InputTokens` | 输入总数，含缓存读写子项 |
| `output_token` | `OutputTokens` | 输出总数，含推理子项 |
| `cache_read_token` | `CacheReadTokens` | `input_token` 的子项 |
| `cache_write_token` | `CacheWriteTokens` | `input_token` 的子项；上游只报单一缓存写数量时使用 |
| `cache_write_5m` | `CacheWrite5mTokens` | `input_token` 的子项；上游能按 TTL 分档时的 5 分钟档 |
| `cache_write_1h` | `CacheWrite1hTokens` | `input_token` 的子项；1 小时档 |
| `reasoning_token` | `ReasoningTokens` | `output_token` 的子项，仅事实记录 |
| `request` | 由落库路径生成 | 请求次数 |

### 子项不叠加

`cache_read_token`、`cache_write_*` 已包含在 `input_token` 内，`reasoning_token` 已包含在
`output_token` 内。它们各自独立计价：

- 定价表里没有某个子项的分量时，该子项不重复计价；
- 定价表里有该子项时，按该分量自己的单价计一次，**不叠加**到主计数上
  （主计数已包含子项，叠加会重复收费）。

缓存写的两套口径互斥：上游能分档（`cache_write_5m` / `cache_write_1h`）时只落两档分量，
不分档的 `cache_write_token` 不落；上游只报单一数量时落 `cache_write_token`。
两者同时非零属异常，以分档为准并记一条冲突日志。

`server_tool_uses`（服务端工具执行次数）当前没有对应的 metric，落库时丢弃并记日志：
丢掉一个没有计价口径的分量，代价远小于丢掉一整行已经发生的转发流水。

### `request` 分量的落库口径

`request` 由落库路径生成，不来自适配器上报：`billing_usage` 一行即一次请求，
该分量的值与窗口内行数恒等。补写发生在落库入口，结算与「无定价占位」两条路径共用同一份分量，
不会一条有 `request`、另一条没有。

历史流水没有该键，聚合侧按 0 计，不回填。

一个请求只写一行流水：

- 非流式在回写客户端**之前**落库；客户端写出失败不回滚流水（上游已经产生过用量）。
- 流式在流进入终态后落库；被截断的流按已收到的用量落库。
- 换渠道重试期间的失败尝试不产生流水，只有最终履约（或最终失败）的那次产生一条。
- 上游未给出可用计数时（来源未知），token 分量全为 0，流水只带 `request: 1`。
- 结算失败时退回占位口径只落用量，并记结构化错误日志。

## 上游失败分类与处置

上游失败的分类（这是什么失败）与处置（接下来做什么）统一由 `internal/failure` 给出，
消费方只读一个入口：`failure.ActionsOf(err)`。

分类名写进尝试日志的 `failure_class`，按类聚合与排障用。

| 分类名 | 触发 | 处置 |
|---|---|---|
| `auth` | 401、403，或报文里的认证/权限字面量 | 停用凭据 + 换凭据 |
| `quota` | 套餐额度、窗口额度、plan 限额用尽 | 停用凭据（15m）+ 换凭据 + 换渠道 |
| `credit` | 余额、授信、欠费停服 | 停用凭据（30m）+ 换凭据 + 换渠道 |
| `rate_limit` | 限流、上游过载 | 换渠道 |
| `request` | 参数、端点、模型名写法等请求级错误 | 不重试 |
| `context_length` | 上下文超出模型窗口 | 不重试 |
| `model_unavailable` | 模型不存在或不可用 | 不重试 |
| `upstream` | 5xx、连接失败、响应无法解析 | 换渠道 + 计入熔断 |
| `timeout` | 408、等待上游响应的超时、流式空闲超时 | 换渠道 + 计入熔断 |
| `other` | 未能归类 | 不重试 |

处置集合分两组：重试动作（`retry_next_account`、`retry_next_route`）与副作用
（`suspend_account`、`count_breaker`）。前者与 `surface`（不重试）互斥；
额度与余额两类同时带两个重试动作，语义是升级顺序：先把同渠道的凭据试完，再换渠道。

分类判据是状态码加报文正则，两者一起看：同一个状态码在不同上游下含义不同，
余额耗尽可能报成 429 配 `insufficient_quota`，只看 429 会把它当成限流退避重试。

报文规则的优先级（上下文超限 → 余额 → 限流 → 额度 → 模型不可用）都有真实报文依据：
限流报文里的 `limit reached` 会命中额度规则，上下文超限里的 `maximum`/`limit` 同理。

流式错误帧与 2xx 里出现的错误信封按同一套机器码归类（`invalid_request_error`、
`insufficient_quota`、`deadline_exceeded` 等）。否定结论的兜底两边不同：
非 2xx 里认不出的错误归 `request`（不能断言该怪谁，保守），
成功响应里出现错误信封则归 `upstream`（报文与状态码自相矛盾，本身就是上游侧故障）。

渠道 config 里的登录态信标头 `signin_header` 覆盖的是**处置**而非分类：

| 信标取值 | 效果 |
|---|---|
| `expired` | 分类定为 `auth`，停用并换凭据，不管状态码是什么 |
| `kept` | 分类如实保留，摘掉整组凭据动作（不停用、也不为它换 key） |
| `renewed` | 同 `kept`，另解除该凭据的既有冷却 |

分类与错误码是两根轴：分类驱动内部处置，错误码决定对外的状态码与错误体形态。
`timeout` 与 `upstream` 的处置完全相同，单独成类只为一件事：前者对应 504、后者对应 502。

动作集给出的是本次失败**值得**做什么，不是最终执行的结果。执行还取决于当时的可行性：
尚未向客户端写出字节时可换渠道，流式下任何一帧（包括只带 role 的首帧）都会关闭这个窗口；
上下文已取消或尝试预算耗尽时同样不重试。

## 错误码表

同一个统一错误码在不同方言下的错误体形态：

- 发生在协议分派**之前或之外**的错误（401、402、429，以及未注册路径的 404）一律使用共享
  `ErrorEnvelope`（`{"error":{"message","type","code"}}`），**与请求方言无关**。
- 已注册路径上由方言适配器编码的错误（400、404、405、5xx）使用该方言的错误体，
  Anthropic 使用 `{"type":"error","error":{"type","message"}}`，
  Gemini 使用 `{"error":{"code","message","status"}}`（`status` 是 gRPC 状态名）。

| HTTP | 错误码 | 触发点 | 错误体 |
|---|---|---|---|
| 400 | `invalid_request` | 请求体非法、缺少必填字段、字段取值非法 | 方言 |
| 401 | `unauthorized` | 缺少或非法 `Authorization`、密钥无效/过期、账户不可用 | 共享 |
| 402 | `forbidden` | 账户无任何可用额度 | 共享 |
| 404 | `model_not_found` | 无可用候选渠道 | 方言 |
| 404 | `not_found` | 未注册路径 | 共享 |
| 405 | `invalid_request` | 已注册路径上的非 POST 方法 | 方言 |
| 429 | `quota_exceeded` | `reject` 处置的窗口限额超限 | 共享 |
| 429 | `rate_limited` | `throttle` 处置的窗口限额超限，附 `Retry-After` | 共享 |
| 500 | `internal` | 平台自身故障：存储查询失败、额度预检失败、编码失败等 | 共享或方言 |
| 502 | `upstream_unavailable` | 上游连接失败、响应无法解析、流在结束标记前断开 | 方言 |
| 502 | `upstream_rejected` | 上游 4xx 拒绝请求 | 方言 |
| 503 | `upstream_rate_limited` | 上游返回 429 | 方言 |
| 504 | `upstream_timeout` | 上游返回 408，或等待上游响应的超时到期 | 方言 |

`Retry-After` 只在 `throttle` 处置下出现，单位秒。

上游错误映射到本表的口径：上游 2xx 之外的响应按状态码分级——
429 → `upstream_rate_limited`，408 → `upstream_timeout`，
其余 4xx → `upstream_rejected`，5xx 与其它 → `upstream_unavailable`。
连接失败、响应体超限、流式单帧超限、结束标记前断开 → `upstream_unavailable`。
流式错误帧与 2xx 里的错误信封按分类推出错误码：`rate_limit` → `upstream_rate_limited`，
`timeout` → `upstream_timeout`，`upstream` → `upstream_unavailable`，其余 → `upstream_rejected`。

错误码不再参与「要不要重试」的判定——那是分类与动作集的职责（见上一节）。

> 错误码枚举里还有 `gateway_overloaded`，当前没有代码路径写出它，故不在表中。
> 新增可对外出现的错误码时，本表与 `docs/openapi.yaml` 的 responses 需同步更新。
