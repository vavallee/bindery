import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'
import { tailwindSafety } from './eslint-rules/tailwind-safety.js'

export default tseslint.config(
  { ignores: ['dist'] },
  {
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    files: ['**/*.{ts,tsx}'],
    languageOptions: {
      ecmaVersion: 2020,
      globals: globals.browser,
    },
    plugins: {
      'react-hooks': reactHooks,
      'react-refresh': reactRefresh,
      'tailwind-safety': tailwindSafety,
    },
    rules: {
      // Two Tailwind bugs that produce no build error and no runtime error —
      // the class is simply absent from the compiled CSS, or the declaration is
      // invalid and dropped. Both shipped undetected; see eslint-rules/.
      'tailwind-safety/no-glued-class-interpolation': 'error',
      'tailwind-safety/no-comma-arbitrary-value': 'error',
      // Explicitly list the two classic hooks rules that were in recommended
      // before eslint-plugin-react-hooks 7.1 added React Compiler rules. The
      // new compiler rules (set-state-in-effect, immutability, purity, etc.)
      // require the React Compiler to be configured and are not applicable to
      // this project.
      'react-hooks/rules-of-hooks': 'error',
      'react-hooks/exhaustive-deps': 'warn',
      'react-refresh/only-export-components': ['warn', { allowConstantExport: true }],
      '@typescript-eslint/no-unused-vars': ['warn', { argsIgnorePattern: '^_' }],
    },
  },
)
