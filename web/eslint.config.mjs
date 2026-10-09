import js from '@eslint/js';
import tseslint from 'typescript-eslint';

export default tseslint.config(
  // 生成物不参与 lint：形状由 scripts/gen.mjs 的模板决定，改它没有意义。
  // 生成物仍进 tsc（npm run build 的类型检查）与漂移核对（npm run gen:check）。
  { ignores: ['dist', 'src/lib/generated'] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    rules: {
      // 未使用变量允许 _ 前缀：解构占位与 React props 丢弃是常见形态。
      '@typescript-eslint/no-unused-vars': [
        'error',
        { argsIgnorePattern: '^_', varsIgnorePattern: '^_' },
      ],
    },
  },
);
