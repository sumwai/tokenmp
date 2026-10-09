#!/usr/bin/env node
/**
 * 契约生成：docs/openapi-web.yaml → src/lib/generated/。
 *
 * 只生成三样东西：契约类型（schema.d.ts）、业务码常量（codes.ts）、按 operationId
 * 的薄请求客户端（api.ts）。页面、状态管理与请求语义不生成 —— 信封解包、令牌注入
 * 与一次性刷新是页面通信的运行时约定，留在 src/lib/client.ts。
 *
 * 用法：
 *   npm run gen         写入生成物
 *   npm run gen:check   只核对：生成物与契约不一致时以非零码退出（CI 门禁）
 *
 * 契约是唯一事实源：类型与业务码都从契约读出，脚本里不重复声明取值。
 */
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import openapiTS, { astToString } from 'openapi-typescript';
import { parse as parseYaml } from 'yaml';

/** 相对本文件定位，脚本从任何工作目录执行都取同一份契约与产物目录。 */
const CONTRACT = new URL('../../docs/openapi-web.yaml', import.meta.url);
const OUT_DIR = new URL('../src/lib/generated/', import.meta.url);

const METHODS = ['get', 'post', 'put', 'delete'];

/** 生成物统一文件头：标注来源与再生成方式，避免被当成手写文件修改。 */
const BANNER = `/**
 * 本文件由 docs/openapi-web.yaml 生成，勿手改。
 * 契约变更后执行 \`npm run gen\`（核对用 \`npm run gen:check\`）。
 */
`;

const doc = parseYaml(await readFile(CONTRACT, 'utf8'));

/** 参数可能是内联声明，也可能 $ref 到 components.parameters。 */
function resolveParam(entry) {
  if (!entry.$ref) {
    return entry;
  }
  const name = entry.$ref.split('/').pop();
  const resolved = doc.components?.parameters?.[name];
  if (!resolved) {
    throw new Error(`契约引用了不存在的参数: ${entry.$ref}`);
  }
  return resolved;
}

/** operationId → 路径、方法、必填项，按 operationId 排序保证生成物稳定。 */
function collectOperations() {
  const operations = [];
  for (const [path, item] of Object.entries(doc.paths ?? {})) {
    for (const method of METHODS) {
      const operation = item?.[method];
      if (!operation) {
        continue;
      }
      const id = operation.operationId;
      if (!id || !/^[A-Za-z][A-Za-z0-9]*$/.test(id)) {
        throw new Error(`${method.toUpperCase()} ${path} 缺少可用的 operationId`);
      }
      const params = [...(item.parameters ?? []), ...(operation.parameters ?? [])].map(resolveParam);
      operations.push({
        id,
        path,
        method,
        summary: operation.summary ?? '',
        // 参数与请求体任一必填时，调用点必须给出请求选项。
        requiresInit: params.some((p) => p.required === true) || operation.requestBody?.required === true,
      });
    }
  }
  const seen = new Set();
  for (const { id } of operations) {
    if (seen.has(id)) {
      throw new Error(`operationId 重复: ${id}`);
    }
    seen.add(id);
  }
  return operations.sort((a, b) => a.id.localeCompare(b.id));
}

/** 契约类型：openapi-typescript 的输出，按契约路径、参数与响应形状生成。 */
async function generateSchema() {
  return BANNER + astToString(await openapiTS(CONTRACT));
}

/** 业务码常量：取值与常量名都取自契约 BusinessCode 的 enum 与 x-enum-varnames。 */
function generateCodes() {
  const schema = doc.components?.schemas?.BusinessCode;
  const names = schema?.['x-enum-varnames'];
  const values = schema?.enum;
  if (!Array.isArray(names) || !Array.isArray(values) || names.length !== values.length) {
    throw new Error('契约缺少 components.schemas.BusinessCode 的 enum / x-enum-varnames');
  }
  const entries = names.map((name, index) => `  ${name}: ${values[index]},`).join('\n');
  return `${BANNER}
/** 业务码：程序按它分支，不解析文案。 */
export const Code = {
${entries}
} as const;

/** 业务码取值集合。 */
export type BusinessCode = (typeof Code)[keyof typeof Code];
`;
}

/** 薄请求客户端：每个 operationId 一个函数，路径、参数与请求体类型来自契约。 */
function generateClient() {
  const entries = collectOperations()
    .map(({ id, path, method, summary, requiresInit }) => {
      const note = summary ? `  /** ${summary} */\n` : '';
      const init = requiresInit ? 'init' : 'init?';
      return `${note}  ${id}: (${init}: ApiInit<"${path}", "${method}">) =>
    apiRequest("${path}", "${method}", init),`;
    })
    .join('\n\n');
  return `${BANNER}
import { apiRequest, type ApiInit } from '../client';

/**
 * 契约生成的薄请求客户端：每个 operationId 一个函数。
 *
 * 路径、参数、请求体与响应类型均来自契约；信封解包、访问令牌注入与一次性刷新
 * 由 ../client.ts 承担 —— 那是页面通信的运行时约定，不是契约内容。
 */
export const api = {
${entries}
};
`;
}

/** firstDiff 给出首个不同的行号与两边的该行内容，便于定位漂移。 */
function firstDiff(expected, actual) {
  const left = expected.split('\n');
  const right = actual.split('\n');
  for (let i = 0; i < Math.max(left.length, right.length); i++) {
    if (left[i] !== right[i]) {
      return `第 ${i + 1} 行:\n  契约生成: ${left[i] ?? '<文件结束>'}\n  磁盘现状: ${right[i] ?? '<文件结束>'}`;
    }
  }
  return '内容一致';
}

const files = {
  'schema.d.ts': await generateSchema(),
  'codes.ts': generateCodes(),
  'api.ts': generateClient(),
};

const check = process.argv.includes('--check');
const stale = [];

for (const [name, content] of Object.entries(files)) {
  const target = new URL(name, OUT_DIR);
  const current = await readFile(target, 'utf8').catch(() => null);
  if (current === content) {
    continue;
  }
  if (check) {
    stale.push(`${name}\n${firstDiff(content, current ?? '')}`);
    continue;
  }
  await mkdir(OUT_DIR, { recursive: true });
  await writeFile(target, content);
  console.log(`写入 src/lib/generated/${name}`);
}

if (check) {
  if (stale.length > 0) {
    console.error(`生成物与契约不一致，执行 npm run gen 后提交：\n${stale.join('\n')}`);
    process.exit(1);
  }
  console.log('生成物与契约一致。');
}
