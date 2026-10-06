# 运营剧本

本文件是运营动作的命令序列版：从商家入驻到余额核对，每一步给出可直接执行的
`tokenmp admin ...` 命令与预期的输出形态。命令只做参数解析与输出，业务校验在
`internal/admin`，数据库连接取自 `TOKENMP_MYSQL_DSN`。

步骤编号与端到端剧本 `cmd/tokenmp/journey_e2e_test.go` 的 7 个子测试一一对应，便于互查：

| 本文件步骤 | e2e 子测试（`make e2e`） |
|---|---|
| 1 入驻、开户、定价与充值 | 步骤 1 迁移就位与入驻发 key 充值买包 |
| 2 启动 serve | 步骤 2 启动 serve 与假上游 |
| 3 客户端调用（三方言） | 步骤 3 三方言流式与非流式 |
| 4 限额与调账 | 步骤 4 限额拦截与重置 |
| 5 凭据轮换 | 步骤 5 凭据轮换 |
| 6 跨协议降级 | 步骤 6 跨协议转换 |
| 7 流水与余额核对 | 步骤 7 admin 回读对账 |

第 4 步的调账命令不在 e2e 剧本内（剧本只演练限额），其余步骤与子测试逐项对应。
`make e2e` 的 DSN 经 `TOKENMP_TEST_MYSQL_DSN` 传入，须指向可丢弃的库。

以下示例取值均为占位，用 `tokenmp` 指代 `./bin/tokenmp`。`id` 一类的数字以实际库为准，
命令用输出里的编号串联后续步骤；所有 `admin` 命令读取 `TOKENMP_MYSQL_DSN`。
所有 `list` 动作输出对齐的纯文本表格，加 `--json` 输出机器可读格式。

## 步骤 1：入驻、开户、定价与充值

对应 e2e 步骤 1。一次把商家、渠道、凭据、模型映射、定价、账户、密钥、充值与商品建齐。

### 1.1 商家入驻

迁移预置一个平台自营商家（id=1），入驻商家从 id=2 起：

```
$ tokenmp admin merchant list
id  code      name      kind      status  created_at
1   platform  平台自营  platform  active  2026-01-01 00:00:00
```

```
$ tokenmp admin merchant create --code partner-1 --name 入驻商家 --kind partner
已创建商家 id=2
```

`--kind` 取 `platform`（平台自营）或 `partner`（入驻商家）。平台自营也是一行数据。
以下步骤用新商家的 id（此处为 2）继续。

停用：`tokenmp admin merchant disable --id 2`，输出 `已停用商家 id=2`。

### 1.2 渠道与凭据

```
$ tokenmp admin channel create --merchant 2 --name upstream-chat \
    --type openai_chat --base-url https://api.example.com/v1 \
    --cred-group grp-chat --vendor vendor-a
已创建渠道 id=1
```

`--type` 取 `openai_chat` / `openai_responses` / `anthropic_messages` / `gemini_generate`，
决定该渠道能服务的协议方言。`--base-url` 只填到端点段之前，端点段由协议决定；
Gemini 的端点含模型名与流式后缀，由适配器在调用上游时拼接，`--base-url` 填到版本根
（如 `https://generativelanguage.googleapis.com/v1beta`）。`--priority` 与 `--weight`
不填默认 100。

`--credential-style` 可选，覆盖该渠道的凭据注入形态：`authorization` / `x-api-key` /
`x-goog-api-key` / `query`；留空按协议现状。`query` 表示把凭据写成上游地址的 `?key=`
查询参数，服务只接受查询参数、不接受凭据请求头的上游（如部分 Gemini 兼容端点）。

```
$ tokenmp admin credential add --merchant 2 --group grp-chat --name primary \
    --api-key sk-upstream-xxx
已写入凭据 id=1
```

`--api-key` 留空时从标准输入读明文；命令行传参会进 shell 历史与进程列表，生产环境用标准
输入更稳。

```
$ tokenmp admin channel list
id  merchant  name           vendor    type         cred_group  base_url                       priority  weight  enabled
1   2         upstream-chat  vendor-a  openai_chat  grp-chat    https://api.example.com/v1     100       100     是

$ tokenmp admin credential list
id  merchant  cred_group  name     prefix     enabled
1   2         grp-chat    primary  sk-upstr…  是
```

凭据明文只在 `credential add` 那一次传入，库中只存哈希与前缀，`credential list` 不输出明文。
停用：`tokenmp admin credential disable --id 1`。

### 1.3 模型映射、定价与规则

模型映射把客户端请求的模型别名接到渠道上：

```
$ tokenmp admin model-map set --channel 1 --model glm-5 \
    --upstream-model up-glm-5 --multiplier 1.5
已写入模型映射 id=1
```

`--multiplier` 是渠道倍率，参与结算时的倍率链；`--overrides` 是可选的上游请求覆盖项
（JSON 对象）。

定价按**上游模型名**发布（结算按履约模型解析）。`--component` 形如
`metric:price:unit_settle:qty`，可重复：

```
$ tokenmp admin price publish --merchant 2 --model up-glm-5 \
    --component input_token:1:token:1 \
    --component output_token:2:token:1
已发布定价 id=1 model=up-glm-5 version=1
```

`--effective` 不填取当前时刻。按模型发布新版本会退役旧版本：

```
$ tokenmp admin price list --merchant 2 --model up-glm-5
id  merchant  model      version  effective_at         retired_at  status
1   2         up-glm-5   1        2026-01-01 00:00:00  -           active
```

规则在定价之外叠加倍率，按范围、指标、时段与日期性质限定：

```
$ tokenmp admin rule add --scope model_map --scope-id 1 --metric output_token \
    --multiplier 2 --time-from 22:00 --time-to 06:00
已写入规则 id=1

$ tokenmp admin rule list --scope model_map --scope-id 1
id  scope      scope_id  metric        multiplier  valid_from  ...  time_from  time_to  ...  priority
1   model_map  1         output_token  2.0000      -           ...  22:00:00   06:00:00 ...  100
```

`--scope` 取 `pricing` / `model_map` / `plan` / `account`，`--scope-id` 是范围实体 id。
`--weekday-mask`（bit0=周一…bit6=周日）与 `--day-kind-mask`
（1=workday 2=weekend 4=holiday 8=makeup_workday）按位过滤。

日历供 `--day-kind-mask` 使用，从标准输入或 `--file` 读 `日期,day_kind` 行：

```
$ printf '2026-01-01,holiday\n' | tokenmp admin calendar import --calendar cn
已导入日历 cn 共 1 天
```

### 1.4 开户与发 key

```
$ tokenmp admin account create --code acct-1 --name 账户 --merchant 2
已创建账户 id=1
```

`--merchant` 不填时账户走平台自营；`--multiplier` 不填默认 1。后续可用
`account set-merchant` 与 `account set-multiplier` 改默认商家与账户倍率。

```
$ tokenmp admin key issue --account 1 --name main
已签发密钥 id=1 account=1 prefix=sk-ab12c
明文（仅此一次，之后不可恢复）：sk-ab12cxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

明文只在签发那一次输出，库中只存哈希与前缀。密钥的 `--merchant` 不填跟随账户默认商家。
废弃：`tokenmp admin key revoke --id 1`，输出 `已吊销密钥 id=1`。

### 1.5 充值、商品与购买

```
$ tokenmp admin bucket credit --account 1 --merchant 2 --unit currency \
    --amount 100 --fallback charge_balance --source recharge
已发放账本 id=1
```

`--unit` 取 `currency` / `token` / `credit`，`--fallback` 取 `charge_balance`（扣尽后转余额）或
`reject`，`--source` 取 `purchase` / `grant` / `recharge`。

```
$ tokenmp admin product create --merchant 2 --name token-package --unit token \
    --qty 1000000 --price 10 --validity 30
已上架商品 id=1

$ tokenmp admin purchase buy --account 1 --product 1 --qty 1 --fallback charge_balance
已购买 purchase=1 bucket=2 数量=1000000 实付=10 单位=token 折算率=0.00001 到期=2026-01-31 00:00:00
```

`purchase buy` 派生出对应的账本并把 `bucket` 编号打到输出里，供后续核对。
`--validity` 是商品有效天数，0 表示不过期。

## 步骤 2：启动 serve

对应 e2e 步骤 2。`serve` 启动时执行数据库迁移，然后监听。

```
$ TOKENMP_MYSQL_DSN='user:password@tcp(db.example:3306)/tokenmp?parseTime=true' \
    ./bin/tokenmp serve
```

环境变量全表与启动顺序见 [docs/deploy.md](deploy.md)。确认就绪：

```
$ curl -sS http://127.0.0.1:8080/healthz
ok
```

`/healthz` 返回 200 与纯文本 `ok`，不经鉴权。转发端点要求
`Authorization: Bearer <key>`。

## 步骤 3：客户端调用（三方言）

对应 e2e 步骤 3。三个方言各覆盖非流式与流式，模型别名用步骤 1.3 配置的 `glm-5`。
`KEY` 取步骤 1.4 `key issue` 输出的明文：

```
$ curl -sS -X POST http://127.0.0.1:8080/v1/chat/completions \
    -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
    -d '{"model":"glm-5","messages":[{"role":"user","content":"你好"}]}'

$ curl -sS -X POST http://127.0.0.1:8080/v1/responses \
    -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
    -d '{"model":"glm-5","input":"你好"}'

$ curl -sS -X POST http://127.0.0.1:8080/v1/messages \
    -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
    -d '{"model":"glm-5","max_tokens":64,"messages":[{"role":"user","content":"你好"}]}'
```

请求体加 `"stream":true` 即流式。预期：同协议命中渠道时状态码 200，响应体是上游原始应答
（流式为上游 SSE 帧逐帧透传）；渠道方言与客户端不一致时由网关重建。端点契约与错误码见
[docs/openapi.yaml](openapi.yaml)，参数处理见 [docs/compatibility.md](compatibility.md)。

每次成功履约写一行用量流水；这些行会在步骤 7 回读核对。

## 步骤 4：限额与调账

对应 e2e 步骤 4。

### 4.1 限额

按 API key 维度限制 `input_token`，窗口为自然日，超限处置为 `reject`：

```
$ tokenmp admin quota add --scope api_key --scope-id 1 --metric input_token \
    --window calendar --period day --limit 1000 --action reject
已写入限额 id=1
```

`--scope` 取 `account` / `api_key` / `channel` / `plan`，`--window` 取
`rolling` / `calendar`，`--period` 取 `5h` / `day` / `week` / `month` / `total`，
`--action` 取 `reject`（429 `quota_exceeded`）或 `throttle`（429 `rate_limited`，附
`Retry-After`）。

窗口用量达到限额即拦截，被拦的请求不写流水。回读已用量与剩余（下表是产生过流量后的形态）：

```
$ tokenmp admin quota list --scope api_key --scope-id 1
id  scope    scope_id  metric       window_kind  period  limit_amount  action  used  remaining
1   api_key  1         input_token  calendar     day     1000          reject  1000  0
```

`quota list` 不填过滤条件列出全部；`--account` 列出某账户的限额。

重置窗口基准（已用量聚合下界推到重置时刻）：

```
$ tokenmp admin quota reset --id 1 --reason 运营重置 --operator ops
已重置限额 quota=1 event=1
```

`--reason` 与 `--operator` 必填。删除限额：`tokenmp admin quota del --id 1`。

### 4.2 调账

调账补记人工的资金调整，正数补扣、负数退费：

```
$ tokenmp admin adjust add --account 1 --amount -20 --reason 退费 --operator ops
已写入调账 id=1

$ tokenmp admin adjust list --account 1
id  account  delta_amount  reason  operator  created_at
1   1        -20.00000000  退费      ops       2026-01-01 00:00:00
```

## 步骤 5：凭据轮换

对应 e2e 步骤 5。同一凭据分组内可放多份启用凭据，按 id 升序轮换取用；上游拒绝凭据
（401 / 403 等）时在同一渠道内换下一条，失败那条进入冷却。报废一份凭据的处置是
「先加新的、再停用旧的」：

```
$ tokenmp admin credential add --merchant 2 --group grp-chat --name secondary \
    --api-key sk-upstream-yyy
已写入凭据 id=2

$ tokenmp admin credential disable --id 1
已停用凭据 id=1
```

```
$ tokenmp admin credential list
id  merchant  cred_group  name       prefix     enabled
1   2         grp-chat    primary    sk-upstr…  否
2   2         grp-chat    secondary  sk-upstr…  是
```

冷却状态是进程内的，`serve` 重启即重置；停用是库中的持久状态。

## 步骤 6：跨协议降级

对应 e2e 步骤 6。客户端方言与渠道方言不一致时，网关按内部统一格式重建请求。用法是给目标
渠道加一条模型映射，客户端用另一方言请求该模型：

```
$ tokenmp admin model-map set --channel 1 --model glm-5-cross \
    --upstream-model up-glm-5 --multiplier 1.5
已写入模型映射 id=2
```

`channel 1` 是 `openai_chat` 渠道；客户端用 Anthropic Messages 方言请求 `glm-5-cross`：

```
$ curl -sS -X POST http://127.0.0.1:8080/v1/messages \
    -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
    -d '{"model":"glm-5-cross","max_tokens":64,"messages":[{"role":"user","content":"你好"}]}'
```

预期 200，响应体是 Anthropic Messages 形态（按客户端方言重建），而上游实际被打到 chat
端点。跨协议是降级：同协议候选始终优先，重建会丢弃内部格式未建模的字段，清单见
[docs/compatibility.md](compatibility.md)。

## 步骤 7：流水与余额核对

对应 e2e 步骤 7。回读流水与账本，与前面步骤的预期数值对账。

```
$ tokenmp admin usage list --account 1
id  created_at           account  channel  model      gross_amount  multiplier  usage                                          settlement
1   2026-01-01 00:00:00  1        1        up-glm-5   26.00000000   1.5000      {"input_token": 12, "output_token": 7, "request": 1}  {"lines": [{"bucket_id": 2, "unit": "token", "qty": "39"}]}
```

```
$ tokenmp admin bucket list --account 1
id  account  merchant  unit      total             remaining         expires_at           fallback        source    priority
1   1        2         currency  100.00000000      100.00000000      -                    charge_balance  recharge  100
2   1        2         token     1000000.00000000  9999961.00000000  2026-01-31 00:00:00  charge_balance  purchase  100
```

核对口径：

- **流水行数 = 成功履约的请求次数**。被限额拦截、被上游拒绝、换渠道期间的失败尝试都不写流水；
  一次请求只写一行。
- **`gross_amount`** 是各计价分量「用量 × 单价」之和；**`multiplier`** 是倍率链的合成结果；
  **`settlement`** 是本次的扣减明细（落了哪些账本、各扣多少），`usage` 是本次用量的分量 JSON。
- **token 账本余量** 满足「初始数量 − Σ 每次结算」。上例单次扣 39 token，一次请求后剩
  9999961，行数每增一行再减 39。定价未命中时退回占位口径只落用量，账本不动。
- **充值账本**：扣费落在 token 单位时不消耗 currency 账本。

```
$ tokenmp admin purchase list --account 1
id  account  merchant  product  qty         price_paid   purchased_at
1   1        2         1        1.00000000  10.00000000  2026-01-01 00:00:00
```

`quota list` 的 `used` 与 `usage list` 的同指标聚合是两条独立读取路径，两者对不上时先查
限额的重置事件（`quota reset` 会移动聚合下界）。
