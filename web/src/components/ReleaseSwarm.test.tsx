import { beforeAll, describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { i18nReady } from '../i18n'
import ReleaseSwarm from './ReleaseSwarm'

beforeAll(async () => {
  await i18nReady
})

describe('ReleaseSwarm', () => {
  it('shows the seeders and peers an indexer reported', () => {
    const { container } = render(<ReleaseSwarm seeders={15} peers={18} />)
    expect(container).toHaveTextContent('· 15 seeders · 18 peers')
  })

  it('uses the singular for one', () => {
    const { container } = render(<ReleaseSwarm seeders={1} peers={1} />)
    expect(container).toHaveTextContent('· 1 seeder · 1 peer')
  })

  // A reported 0 is a dead torrent, the case a high grab count hides, so it
  // must show rather than read as unreported.
  it('shows and flags a reported zero', () => {
    render(<ReleaseSwarm seeders={0} peers={0} />)
    expect(screen.getByText('0 seeders')).toHaveClass('text-red-700')
    expect(screen.getByText(/0 peers/)).toBeInTheDocument()
  })

  // Usenet releases and indexers that leave the attributes out carry neither
  // count, and their rows must read exactly as they did before.
  it('renders nothing when neither count was reported', () => {
    const { container } = render(<ReleaseSwarm />)
    expect(container).toBeEmptyDOMElement()
  })

  it('shows seeders alone when peers were not reported', () => {
    const { container } = render(<ReleaseSwarm seeders={4} />)
    expect(container).toHaveTextContent('· 4 seeders')
    expect(container).not.toHaveTextContent('peer')
  })
})
