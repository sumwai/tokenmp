// 把内联在正文里的 <think>…</think> 推理搬到 reasoning_content。
//
// 部分上游（MiniMax 官方 OpenAI 兼容端点即如此）把推理内联在 content 里，且不提供
// reasoning_content。这份中间件两条路径都覆盖：流式逐帧改写，非流式在 JSON 上搬家。
//
// 流式的关键在于缓冲策略：onEvent 一次只能回一个分片，而一帧可能同时含推理尾与正文头
// （MiniMax 就是 "</think>\n\n2+2 = 4" 一帧），所以正文先寄存在 carry 里，跟下一帧一起发；
// 另外只能缓冲「可能是个不完整标签的后缀」——把整个思考块攼到 </think> 才发，客户端会在
// 整个推理阶段收不到字节。
//
// scope 限定它只对 MiniMax 渠道生效：越界的请求不进 JS 运行时，而不是进去了再原样返回。
// 渠道的 --vendor 标签改了就等于换了作用域，serve 启动时会因匹配不上而告警。
export const scope = { vendors: ["minimax"] };
export const events = ["text_delta"];

const OPEN = "<think>";
const CLOSE = "</think>";

// 单请求状态：mode 是 open / think / text，buf 是待处理尾巴，carry 是待转交的正文。
function st(ctx) {
  return (ctx.state.tt ??= { mode: "open", buf: "", carry: "" });
}

// 把寄存的正文接回本帧。
function take(s, text) {
  const out = s.carry + text;
  s.carry = "";
  return out;
}

export function onEvent(event, ctx) {
  if (event.kind !== "text_delta") return event;
  const s = st(ctx);
  s.buf += event.text_delta;

  if (s.mode === "open") {
    // 还看不出是不是标签开头：先攼着，等下一帧。
    if (s.buf.length < OPEN.length && OPEN.startsWith(s.buf)) return null;
    s.mode = s.buf.startsWith(OPEN) ? "think" : "text";
    if (s.mode === "think") s.buf = s.buf.slice(OPEN.length);
  }

  if (s.mode === "text") {
    const out = take(s, s.buf);
    s.buf = "";
    return out === "" ? null : { kind: "text_delta", text_delta: out };
  }

  const at = s.buf.indexOf(CLOSE);
  if (at < 0) {
    // 只在确信不是标签前缀的范围内下发，末尾留 CLOSE.length-1 个字符放着。
    const keep = CLOSE.length - 1;
    if (s.buf.length <= keep) return null;
    const emit = s.buf.slice(0, s.buf.length - keep);
    s.buf = s.buf.slice(s.buf.length - keep);
    return { kind: "reasoning_delta", text_delta: emit };
  }

  const reasoning = s.buf.slice(0, at);
  s.carry = s.buf.slice(at + CLOSE.length);
  s.buf = "";
  s.mode = "text";
  if (reasoning !== "") return { kind: "reasoning_delta", text_delta: reasoning };
  const out = take(s, "");
  return out === "" ? null : { kind: "text_delta", text_delta: out };
}

// 流末冲刷：这里发的分片会出现在终止帧之前。
export function onStreamEnd(ctx) {
  const s = st(ctx);
  const out = take(s, s.buf);
  const kind = s.mode === "think" ? "reasoning_delta" : "text_delta";
  s.buf = "";
  return out === "" ? [] : [{ kind, text_delta: out }];
}

// 非流式没有逐帧缓冲问题，直接在 JSON 上搬家。
export function onResponse(body, ctx) {
  const message = body && body.choices && body.choices[0] && body.choices[0].message;
  if (!message || typeof message.content !== "string") return body;
  let content = message.content;
  let reasoning = "";
  for (;;) {
    const open = content.indexOf(OPEN);
    if (open < 0) break;
    const close = content.indexOf(CLOSE, open + OPEN.length);
    if (close < 0) {
      reasoning += content.slice(open + OPEN.length);
      content = content.slice(0, open);
      break;
    }
    reasoning += content.slice(open + OPEN.length, close);
    content = content.slice(0, open) + content.slice(close + CLOSE.length);
  }
  if (reasoning === "") return body;
  message.content = content.replace(/^\s+/, "");
  message.reasoning_content = (message.reasoning_content || "") + reasoning;
  return body;
}
