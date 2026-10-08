import type { InputHTMLAttributes } from 'react'

// Attribute bundles for inputs whose values are identifiers rather than prose.
// Mobile keyboards otherwise capitalise the first letter, autocorrect, and
// underline "misspellings" in usernames, URLs and keys, and password managers
// treat any type=password field as the login password.

type InputAttrs = InputHTMLAttributes<HTMLInputElement>

// Usernames, hosts and other literal tokens: no capitalisation or correction.
export const literalInputAttrs = {
  autoCapitalize: 'none',
  autoCorrect: 'off',
  spellCheck: false,
} satisfies InputAttrs

// Full URLs and bare hosts. inputMode rather than type="url": several of these
// fields accept host:port without a scheme, which native URL validation would
// reject on form submit.
export const urlInputAttrs = {
  ...literalInputAttrs,
  inputMode: 'url',
} satisfies InputAttrs

// API keys, tokens and client secrets. new-password stops browsers and password
// managers from autofilling the saved Bindery login into the field and from
// offering to save the key as that login's password.
export const secretInputAttrs = {
  ...literalInputAttrs,
  autoComplete: 'new-password',
  'data-1p-ignore': 'true',
  'data-lpignore': 'true',
} satisfies InputAttrs & Record<`data-${string}`, string>

// Port numbers. Kept as text inputs so an empty value stays valid.
export const portInputAttrs = {
  inputMode: 'numeric',
  pattern: '[0-9]*',
} satisfies InputAttrs
