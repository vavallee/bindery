/**
 * Local ESLint rules for two Tailwind failure modes that are invisible at
 * runtime: the class name looks correct in the DOM and the build succeeds, but
 * the CSS rule is either never generated or generated invalid. Nothing else in
 * the toolchain catches either one.
 *
 * No plugin dependency — these are flat-config inline rules.
 */

/** Does this node sit somewhere that ends up in a class attribute? */
function inClassNameContext(node) {
  let child = node
  let parent = node.parent
  // Walk up through expression wrappers (ternaries, &&, nested templates, and
  // clsx()-style calls) to find what the string is ultimately assigned to.
  while (parent) {
    switch (parent.type) {
      case 'JSXExpressionContainer':
        if (
          parent.parent?.type === 'JSXAttribute' &&
          /^class(Name)?$/.test(parent.parent.name?.name ?? '')
        ) {
          return true
        }
        return false
      case 'VariableDeclarator':
        return /(^|[a-z])(cls|class|classes|className)$/i.test(parent.id?.name ?? '')
      case 'Property':
        return /(^|[a-z])(cls|class|classes|className)$/i.test(
          parent.key?.name ?? parent.key?.value ?? '',
        )
      case 'ConditionalExpression':
      case 'LogicalExpression':
      case 'TemplateLiteral':
      case 'CallExpression':
      case 'ArrayExpression':
      case 'ReturnStatement':
      case 'ArrowFunctionExpression':
        child = parent
        parent = parent.parent
        continue
      default:
        return false
    }
  }
  return false
}

/**
 * Flags `max-w-5xl${cond ? …}` — a class token glued directly to an
 * interpolation. Tailwind v4's scanner reads source text, not runtime values,
 * so it extracts the token `max-w-5xl${cond` and never emits `.max-w-5xl`.
 * The page silently loses the class.
 */
const noGluedClassInterpolation = {
  meta: {
    type: 'problem',
    docs: {
      description:
        'Disallow a Tailwind class immediately followed by ${…}; the scanner cannot extract it',
    },
    fixable: 'whitespace',
    schema: [],
    messages: {
      glued:
        "Tailwind cannot extract '{{token}}' because '${' follows it with no space. " +
        'The class will be missing from the compiled CSS. Add a space before ${ ' +
        'and move it inside the expression.',
    },
  },
  create(context) {
    return {
      TemplateLiteral(node) {
        if (node.expressions.length === 0) return
        if (!inClassNameContext(node)) return

        node.quasis.forEach((quasi, i) => {
          // The last quasi has no interpolation after it.
          if (i >= node.expressions.length) return
          const raw = quasi.value.raw
          if (raw === '' || /\s$/.test(raw)) return

          const token = raw.split(/\s/).pop()
          // A trailing separator means the interpolation supplies a value
          // (`/book/${id}`, `bg-${color}`), not a class boundary.
          if (/[-:/[(,._]$/.test(token)) return

          context.report({
            node: quasi,
            messageId: 'glued',
            data: { token },
            fix: fixer => fixer.insertTextAfterRange([quasi.range[0], quasi.range[1] - 2], ' '),
          })
        })
      },
    }
  },
}

/**
 * Flags `grid-cols-[92px,1fr]` — a comma at the top level of an arbitrary
 * value. Tailwind emits the comma literally, producing
 * `grid-template-columns:92px,1fr`, which is invalid CSS and gets dropped.
 * Commas nested inside parens are legal (`bg-[rgb(255,0,0)]`).
 */
const noCommaArbitraryValue = {
  meta: {
    type: 'problem',
    docs: {
      description:
        'Disallow a top-level comma in a Tailwind arbitrary value; use _ as the separator',
    },
    schema: [],
    messages: {
      comma:
        "Arbitrary value '[{{value}}]' has a top-level comma. Tailwind emits it " +
        'literally, producing invalid CSS that the browser drops. Use _ instead.',
    },
  },
  create(context) {
    function check(node, text) {
      if (!text || !text.includes('[')) return
      // Scan each `-[...]` arbitrary value, tracking paren depth so commas
      // inside rgb()/calc()/etc. are left alone.
      const re = /-\[([^\]]*)\]/g
      let m
      while ((m = re.exec(text)) !== null) {
        const value = m[1]
        let depth = 0
        for (const ch of value) {
          if (ch === '(') depth++
          else if (ch === ')') depth--
          else if (ch === ',' && depth === 0) {
            context.report({ node, messageId: 'comma', data: { value } })
            break
          }
        }
      }
    }
    return {
      Literal(node) {
        if (typeof node.value === 'string') check(node, node.value)
      },
      TemplateElement(node) {
        check(node, node.value.raw)
      },
    }
  },
}

export const tailwindSafety = {
  rules: {
    'no-glued-class-interpolation': noGluedClassInterpolation,
    'no-comma-arbitrary-value': noCommaArbitraryValue,
  },
}
