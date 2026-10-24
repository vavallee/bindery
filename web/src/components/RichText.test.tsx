import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import RichText from './RichText'

describe('RichText', () => {
  it('maps tags to the given elements and keeps the text around them', () => {
    const { container } = render(
      <p><RichText text="Run <code>calibredb</code> or read the <guide>guide</guide>." components={{ code: <code />, guide: <a href="/g" /> }} /></p>,
    )
    expect(container.innerHTML).toBe('<p>Run <code>calibredb</code> or read the <a href="/g">guide</a>.</p>')
  })

  it('renders an unmapped tag, and any markup in it, as plain text', () => {
    const { container } = render(<p><RichText text={'<b>bold</b> <img src=x onerror=1>'} components={{}} /></p>)
    expect(container.textContent).toBe('<b>bold</b> <img src=x onerror=1>')
    expect(container.querySelector('b, img')).toBeNull()
  })
})
