import { useEffect, useState } from 'react'
import { api, Author, MetadataProfile, RootFolder } from '../api/client'

interface Props {
  onClose: () => void
  onAdded: () => void
}

export default function AddAuthorModal({ onClose, onAdded }: Props) {
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<Author[]>([])
  const [searching, setSearching] = useState(false)
  const [adding, setAdding] = useState<string | null>(null)
  const [profiles, setProfiles] = useState<MetadataProfile[]>([])
  const [profileId, setProfileId] = useState<number | null>(null)
  const [rootFolders, setRootFolders] = useState<RootFolder[]>([])
  const [rootFolderId, setRootFolderId] = useState<number | null>(null)
  const [searchOnAdd, setSearchOnAdd] = useState(true)
  const [searchError, setSearchError] = useState('')

  useEffect(() => {
    api.listMetadataProfiles().then(ps => {
      setProfiles(ps)
      // Default to the first profile — which is the seeded "Standard"
      // profile on a fresh install — so the language filter kicks in
      // without the user having to pick one.
      if (ps.length > 0) setProfileId(ps[0].id)
    }).catch(console.error)
    api.listRootFolders().then(rfs => {
      setRootFolders(rfs)
      if (rfs.length > 0) setRootFolderId(rfs[0].id)
    }).catch(console.error)
  }, [])

  const search = async () => {
    if (!query.trim()) return
    setSearching(true)
    setSearchError('')
    try {
      const authors = await api.searchAuthors(query)
      setResults(authors)
    } catch (err) {
      setSearchError(err instanceof Error ? err.message : 'Search failed — try again')
    } finally {
      setSearching(false)
    }
  }

  const addAuthor = async (author: Author) => {
    setAdding(author.foreignAuthorId)
    try {
      await api.addAuthor({
        foreignAuthorId: author.foreignAuthorId,
        authorName: author.authorName,
        monitored: true,
        searchOnAdd,
        metadataProfileId: profileId,
        rootFolderId: rootFolderId,
      })
      onAdded()
      onClose()
    } catch (err: unknown) {
      alert(err instanceof Error ? err.message : 'Failed to add author')
    } finally {
      setAdding(null)
    }
  }

  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center p-4 z-50" onClick={onClose}>
      <div className="bg-slate-100 dark:bg-zinc-900 border border-slate-300 dark:border-zinc-700 rounded-lg w-full max-w-lg shadow-2xl max-h-[90vh] flex flex-col" onClick={e => e.stopPropagation()}>
        <div className="p-4 border-b border-slate-200 dark:border-zinc-800">
          <h3 className="text-lg font-semibold">Add Author</h3>
        </div>

        <div className="p-4 flex-1 overflow-y-auto">
          {profiles.length > 1 && (
            <div className="mb-3">
              <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Metadata profile</label>
              <select
                value={profileId ?? ''}
                onChange={e => setProfileId(e.target.value ? Number(e.target.value) : null)}
                className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
              >
                {profiles.map(p => (
                  <option key={p.id} value={p.id}>{p.name}</option>
                ))}
              </select>
            </div>
          )}
          {rootFolders.length > 0 && (
            <div className="mb-3">
              <label className="block text-xs text-slate-600 dark:text-zinc-400 mb-1">Root folder</label>
              <select
                value={rootFolderId ?? ''}
                onChange={e => setRootFolderId(e.target.value ? Number(e.target.value) : null)}
                className="w-full bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
              >
                {rootFolders.map(rf => (
                  <option key={rf.id} value={rf.id}>{rf.path}</option>
                ))}
              </select>
            </div>
          )}
          <label className="flex items-start gap-2 text-sm mb-3 cursor-pointer select-none">
            <input
              type="checkbox"
              checked={searchOnAdd}
              onChange={e => setSearchOnAdd(e.target.checked)}
              className="accent-emerald-500 mt-0.5 flex-shrink-0"
            />
            <span>
              <span className="font-medium">Auto-grab books on add</span>
              <span className="block text-xs text-slate-600 dark:text-zinc-400 mt-0.5">Bindery will always fetch the book catalogue. Enable this to also queue downloads immediately.</span>
            </span>
          </label>

          <div className="flex gap-2">
            <input
              type="text"
              value={query}
              onChange={e => setQuery(e.target.value)}
              onKeyDown={e => e.key === 'Enter' && search()}
              placeholder="Search by author name..."
              className="flex-1 bg-slate-200 dark:bg-zinc-800 border border-slate-300 dark:border-zinc-700 rounded-md px-3 py-2 text-sm focus:outline-none focus:border-emerald-500"
              autoFocus
            />
            <button
              onClick={search}
              disabled={searching}
              className="px-4 py-2 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded-md text-sm font-medium"
            >
              {searching ? 'Searching...' : 'Search'}
            </button>
          </div>

          <div className="mt-4 max-h-80 overflow-y-auto space-y-2">
            {results.map(author => (
              <div
                key={author.foreignAuthorId}
                className="flex items-center justify-between p-3 rounded-md bg-slate-200/50 dark:bg-zinc-800/50 hover:bg-slate-200 dark:hover:bg-zinc-800"
              >
                <div className="min-w-0">
                  <div className="font-medium text-sm">{author.authorName}</div>
                  <div className="text-xs text-slate-600 dark:text-zinc-500 flex flex-wrap gap-x-3">
                    {author.disambiguation && <span>Top work: {author.disambiguation}</span>}
                    {author.statistics?.bookCount ? <span>{author.statistics.bookCount} books</span> : null}
                    {author.ratingsCount ? <span>{author.ratingsCount} ratings</span> : null}
                  </div>
                </div>
                <button
                  onClick={() => addAuthor(author)}
                  disabled={adding === author.foreignAuthorId}
                  className="px-3 py-1 bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50 rounded text-xs font-medium"
                >
                  {adding === author.foreignAuthorId ? 'Adding...' : 'Add'}
                </button>
              </div>
            ))}
            {searchError && (
              <p className="text-sm text-red-400 text-center py-4">Could not reach the metadata provider — {searchError}</p>
            )}
            {results.length === 0 && !searching && !searchError && query && (
              <p className="text-sm text-slate-600 dark:text-zinc-500 text-center py-4">No results found</p>
            )}
          </div>
        </div>

        <div className="p-4 border-t border-slate-200 dark:border-zinc-800 flex justify-end">
          <button onClick={onClose} className="px-4 py-2 text-sm text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">Cancel</button>
        </div>
      </div>
    </div>
  )
}
