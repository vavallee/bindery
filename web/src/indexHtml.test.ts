import { afterEach, describe, expect, it } from 'vitest'
import { act, renderHook } from '@testing-library/react'
// Vite's ?raw import rather than node:fs, so this needs no @types/node.
import html from '../index.html?raw'
import manifestSource from '../public/manifest.webmanifest?raw'
import appSource from './App.tsx?raw'
import bulkActionBarSource from './components/BulkActionBar.tsx?raw'
import whatsNewSource from './components/WhatsNewToast.tsx?raw'
import { THEME_COLORS, useTheme } from './theme'

// Mobile and install metadata in index.html, and the web app manifest it
// links. The Go side (cmd/bindery/spa_test.go) checks the manifest is served
// as application/manifest+json under a URL base.
//
// index.html carries no comments, since they would ship to every visitor.
// The reasoning behind its tags lives here instead:
//   - The manifest link is crossorigin="use-credentials" because manifests are
//     fetched in CORS mode without cookies by default, which an auth proxy in
//     front of Bindery would reject.
//   - The manifest's start_url and scope are "." so they resolve against the
//     manifest URL, which the injected <base> puts under the URL base.
//   - viewport-fit=cover only ships together with the env(safe-area-inset-*)
//     padding in index.css on the header and the fixed bottom bars.
//   - There is one theme-color meta per OS colour scheme. theme-bootstrap.js
//     (before the first paint) and theme.ts (on every toggle) rewrite both to
//     the in-app theme, so an override of the OS also recolours the browser
//     bar.

const tag = (pattern: RegExp) => html.match(pattern)?.[0]
const publicFiles = import.meta.glob('../public/*', { query: '?url', import: 'default', eager: true })
const hasPublicFile = (name: string) => `../public/${name}` in publicFiles

describe('index.html mobile metadata', () => {
  it('ships no HTML comments', () => {
    expect(html).not.toContain('<!--')
  })

  it('has a theme-color meta per colour scheme matching the theme backgrounds', () => {
    const light = tag(/<meta name="theme-color"[^>]*prefers-color-scheme: light[^>]*>/)
    const dark = tag(/<meta name="theme-color"[^>]*prefers-color-scheme: dark[^>]*>/)
    expect(light).toContain(`content="${THEME_COLORS.light}"`)
    expect(dark).toContain(`content="${THEME_COLORS.dark}"`)
  })

  it('links an apple-touch-icon that exists', () => {
    const link = tag(/<link rel="apple-touch-icon"[^>]*>/)
    expect(link).toBeDefined()
    const href = link!.match(/href="\/?([^"]+)"/)![1]
    expect(hasPublicFile(href), href).toBe(true)
  })

  it('links the manifest with credentials, so it loads behind an auth proxy', () => {
    const link = tag(/<link rel="manifest"[^>]*>/)
    expect(link).toBeDefined()
    expect(link).toContain('crossorigin="use-credentials"')
    // Root relative in source; Vite rewrites it to ./ so the <base> tag
    // resolves it under a URL base. A cross-origin or absolute URL would not.
    expect(link).toMatch(/href="\/manifest\.webmanifest"/)
  })

  it('only covers the display cutout alongside safe area padding', () => {
    // viewport-fit=cover without env(safe-area-inset-*) padding puts the
    // header and the fixed bottom bars under the notch and home indicator.
    const viewport = tag(/<meta name="viewport"[^>]*>/)
    expect(viewport).toContain('viewport-fit=cover')
    // The *-safe utilities are defined in index.css (vitest stubs CSS imports,
    // so that file cannot be read from here).
    expect(appSource).toMatch(/<header className="[^"]*\bpt-safe\b/)
    expect(bulkActionBarSource).toMatch(/fixed bottom-0[^"]*\bpb-safe-\d/)
    expect(whatsNewSource).toMatch(/fixed bottom-safe-\d right-safe-\d/)
  })
})

describe('web app manifest', () => {
  const manifest = JSON.parse(manifestSource) as {
    name: string
    short_name: string
    start_url: string
    scope: string
    display: string
    icons: { src: string; sizes: string; type: string; purpose: string }[]
  }

  it('describes an installable standalone app', () => {
    expect(manifest.name).toBe('Bindery')
    expect(manifest.short_name).toBeTruthy()
    expect(manifest.display).toBe('standalone')
  })

  it('uses relative start_url and scope so a URL base mount keeps working', () => {
    // Resolved against the manifest URL, so /bindery/manifest.webmanifest
    // yields /bindery/ for both. A leading slash would escape the base.
    expect(manifest.start_url).toBe('.')
    expect(manifest.scope).toBe('.')
  })

  it('lists 192 and 512 icons that exist, by relative path', () => {
    const sizes = manifest.icons.map(icon => icon.sizes)
    expect(sizes).toEqual(expect.arrayContaining(['192x192', '512x512']))
    for (const icon of manifest.icons) {
      expect(icon.src.startsWith('/'), icon.src).toBe(false)
      expect(hasPublicFile(icon.src), icon.src).toBe(true)
    }
  })

  it('has a padded maskable icon alongside the plain "any" ones', () => {
    // The maskable variant keeps the glyph inside the central safe zone on a
    // full bleed background, so launchers can crop it to any shape. The
    // round favicon would be clipped if it were offered as maskable.
    const maskable = manifest.icons.filter(icon => icon.purpose === 'maskable')
    expect(maskable.map(icon => icon.sizes)).toContain('512x512')
    expect(maskable.map(icon => icon.src)).not.toContain('favicon.png')
    const any = manifest.icons.filter(icon => icon.purpose === 'any')
    expect(any.map(icon => icon.sizes)).toEqual(expect.arrayContaining(['192x192', '512x512']))
  })
})

describe('theme-color follows the in-app theme', () => {
  afterEach(() => {
    document.head.querySelectorAll('meta[name="theme-color"]').forEach(meta => meta.remove())
    localStorage.removeItem('bindery.theme')
  })

  it('rewrites every theme-color meta when the toggle overrides the OS', () => {
    for (const scheme of ['light', 'dark'] as const) {
      const meta = document.createElement('meta')
      meta.name = 'theme-color'
      meta.media = `(prefers-color-scheme: ${scheme})`
      meta.content = THEME_COLORS[scheme]
      document.head.appendChild(meta)
    }
    localStorage.setItem('bindery.theme', 'light')
    const { result } = renderHook(() => useTheme())
    const contents = () => [...document.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]')].map(m => m.content)

    expect(contents()).toEqual([THEME_COLORS.light, THEME_COLORS.light])
    act(() => result.current.setTheme('dark'))
    expect(contents()).toEqual([THEME_COLORS.dark, THEME_COLORS.dark])
  })
})
