// Regenerates docs/screenshots from a throwaway instance. Run it through
// `make screenshots`, which builds the Linux binaries this boots and runs it in
// a container, so the browser and fonts are the same on every machine.
//
// Nothing here reaches the network: the database is temporary, the seed
// (main.go) has no real images, and covers are drawn in fulfil() below.
import { chromium } from 'playwright'
import { spawn, execFileSync } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import fs from 'node:fs'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'

const root = path.resolve(import.meta.dirname, '../..')
const out = path.join(root, 'docs/screenshots')
const bin = path.join(import.meta.dirname, '.out')
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'bindery-screenshots-'))

const DESKTOP = { viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 }
const MOBILE = { viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true }
const TOUR = { viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1 }

// The file names and viewports the README uses.
const SHOTS = [
  ['authors', '/', ['desktop-dark', 'mobile-dark', 'mobile-light']],
  ['author-detail', 'AUTHOR', ['desktop-dark', 'mobile-dark', 'mobile-light']],
  ['books', '/books', ['desktop-dark', 'mobile-dark', 'mobile-light']],
  ['calendar', '/calendar', ['desktop-dark']],
  ['queue', '/queue', ['desktop-dark']],
]

// The tour GIF: page, scroll position, seconds on screen.
const TOUR_FRAMES = [
  ['/', 0, 2],
  ['AUTHOR', 0, 3],
  ['AUTHOR', 100, 2],
  ['/books', 0, 3],
  ['/books', 400, 2.5],
  ['/calendar', 0, 3],
  ['/queue', 0, 3],
]

const COLOURS = [
  ['#2f6f5e', '#152f47'], ['#352f73', '#3a1a42'], ['#55702c', '#1a4217'], ['#6f2f42', '#42241a'],
  ['#6f3a2c', '#423a1a'], ['#2c6070', '#1a2747'], ['#702c5c', '#42171f'], ['#3a702c', '#173f30'],
]

const hue = text => COLOURS[[...text].reduce((sum, c) => (sum * 31 + c.codePointAt(0)) % 9973, 7) % COLOURS.length]
const escape = text => text.replace(/&/g, '&amp;').replace(/</g, '&lt;')

// Wrap a title to the width of a cover, in lines of at most 16 characters.
function wrap(title) {
  const lines = ['']
  for (const word of title.split(' ')) {
    const line = lines[lines.length - 1]
    if (line && line.length + word.length >= 16) lines.push(word)
    else lines[lines.length - 1] = line ? `${line} ${word}` : word
  }
  return lines
}

function coverSVG(title, by) {
  const [from, to] = hue(title)
  const lines = wrap(title)
  const text = lines.map((line, i) =>
    `<text x="208" y="${230 + i * 46 - (lines.length - 1) * 23}" font-size="36" font-weight="700" fill="#fff" text-anchor="middle" font-family="Georgia,serif">${escape(line)}</text>`).join('')
  return `<svg xmlns="http://www.w3.org/2000/svg" width="400" height="600" viewBox="0 0 400 600">
<defs><linearGradient id="g" x2="1" y2="1"><stop stop-color="${from}"/><stop offset="1" stop-color="${to}"/></linearGradient></defs>
<rect width="400" height="600" fill="url(#g)"/><rect width="20" height="600" fill="#000" opacity=".25"/>
<rect x="40" y="110" width="336" height="1" fill="#fff" opacity=".3"/>${text}
<text x="208" y="540" font-size="20" fill="#fff" opacity=".7" text-anchor="middle" font-family="Helvetica,Arial,sans-serif">${escape(by)}</text></svg>`
}

function avatarSVG(name) {
  const [from, to] = hue(name)
  const initials = name.split(' ').filter(w => !w.endsWith('.') || w === name.split(' ')[0]).map(w => w[0])
  return `<svg xmlns="http://www.w3.org/2000/svg" width="256" height="256" viewBox="0 0 256 256">
<defs><linearGradient id="g" x2="1" y2="1"><stop stop-color="${from}"/><stop offset="1" stop-color="${to}"/></linearGradient></defs>
<rect width="256" height="256" fill="url(#g)"/>
<text x="128" y="164" font-size="104" font-weight="700" fill="#fff" text-anchor="middle" font-family="Helvetica,Arial,sans-serif">${escape(initials[0] + initials[initials.length - 1])}</text></svg>`
}

// The app asks its image proxy for every picture; answer here instead.
async function fulfil(route) {
  const image = new URL(new URL(route.request().url()).searchParams.get('url'))
  const title = image.searchParams.get('title')
  const body = image.pathname === '/avatar' ? avatarSVG(title) : coverSVG(title, image.searchParams.get('by'))
  await route.fulfill({ contentType: 'image/svg+xml', body })
}

function freePort() {
  return new Promise((resolve, reject) => {
    const probe = net.createServer().once('error', reject).listen(0, '127.0.0.1', () => {
      const { port } = probe.address()
      probe.close(() => resolve(port))
    })
  })
}

async function waitForHealth(base, server) {
  for (let i = 0; i < 100; i++) {
    if (server.exitCode !== null) throw new Error(`bindery exited with ${server.exitCode}`)
    const ok = await fetch(`${base}/api/v1/health`).then(r => r.ok, () => false)
    if (ok) return
    await new Promise(r => setTimeout(r, 100))
  }
  throw new Error('bindery did not become healthy')
}

async function open(browser, state, device, theme) {
  const context = await browser.newContext({ ...device, colorScheme: theme, locale: 'en-US', storageState: state })
  await context.addInitScript(t => {
    localStorage.setItem('bindery.theme', t)
    // The demo has no indexer or client; the setup prompts would top every page.
    localStorage.setItem('bindery.setupBannerDismissed', 'indexer+client')
    localStorage.setItem('bindery.setupChecklistDismissed', '1')
  }, theme)
  await context.route('**/api/v1/images?*', fulfil)
  return { context, page: await context.newPage() }
}

async function show(page, url, scroll = 0) {
  await page.goto(url)
  await page.waitForLoadState('networkidle')
  await page.evaluate(y => window.scrollTo(0, y), scroll)
  await page.evaluate(() => document.fonts.ready)
  // Covers below the fold are lazy and never finish; only the visible ones count.
  await page.waitForFunction(() => [...document.images]
    .filter(i => i.getBoundingClientRect().top < window.innerHeight)
    .every(i => i.complete))
}

async function tour(browser, state, base, authorPath) {
  const { context, page } = await open(browser, state, TOUR, 'dark')
  const list = []
  for (const [i, [target, scroll, seconds]] of TOUR_FRAMES.entries()) {
    const file = path.join(tmp, `frame-${i}.png`)
    await show(page, base + (target === 'AUTHOR' ? authorPath : target), scroll)
    await page.screenshot({ path: file })
    list.push(`file '${file}'`, `duration ${seconds}`)
  }
  await context.close()
  // The concat demuxer ignores the last duration unless the frame is repeated.
  list.push(list[list.length - 2])
  const frames = path.join(tmp, 'frames.txt')
  fs.writeFileSync(frames, list.join('\n'))
  execFileSync('ffmpeg', ['-v', 'error', '-y', '-f', 'concat', '-safe', '0', '-i', frames,
    '-vf', 'fps=20,split[a][b];[a]palettegen=stats_mode=diff[p];[b][p]paletteuse=dither=none',
    '-loop', '0', path.join(out, 'loop.gif')])
}

const port = await freePort()
const base = `http://127.0.0.1:${port}`
const dbPath = path.join(tmp, 'bindery.db')
execFileSync(path.join(bin, 'seed'), [dbPath], { stdio: 'inherit' })

const server = spawn(path.join(bin, 'bindery'), {
  stdio: 'ignore',
  env: { ...process.env, BINDERY_PORT: String(port), BINDERY_DB_PATH: dbPath, BINDERY_DATA_DIR: path.join(tmp, 'data') },
})
const browser = await chromium.launch()
try {
  await waitForHealth(base, server)
  const auth = await browser.newContext()
  const credentials = { username: 'demo', password: randomUUID() }
  for (const step of ['setup', 'login']) {
    const res = await auth.request.post(`${base}/api/v1/auth/${step}`, { data: credentials })
    if (!res.ok()) throw new Error(`${step}: ${res.status()} ${await res.text()}`)
  }
  const state = await auth.storageState()
  const authors = await (await auth.request.get(`${base}/api/v1/author`)).json()
  // The page the README shows is the author with the most books.
  const authorId = (authors.items ?? authors).find(a => a.authorName === 'Brandon Sanderson').id
  const authorPath = `/author/${authorId}`

  for (const [name, target, variants] of SHOTS) {
    for (const variant of variants) {
      const [form, theme] = variant.split('-')
      const { context, page } = await open(browser, state, form === 'mobile' ? MOBILE : DESKTOP, theme)
      await show(page, base + (target === 'AUTHOR' ? authorPath : target))
      await page.screenshot({ path: path.join(out, `${name}-${variant}.png`) })
      await context.close()
      console.log(`${name}-${variant}.png`)
    }
  }
  await tour(browser, state, base, authorPath)
  console.log('loop.gif')
} finally {
  await browser.close()
  server.kill()
  fs.rmSync(tmp, { recursive: true, force: true })
}
