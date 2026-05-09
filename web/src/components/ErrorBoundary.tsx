import { Component, type ErrorInfo, type ReactNode } from 'react'

type Props = { children: ReactNode }
type State = { hasError: boolean; error: Error | null; errorInfo: ErrorInfo | null; showDetails: boolean }

export default class ErrorBoundary extends Component<Props, State> {
  state: State = { hasError: false, error: null, errorInfo: null, showDetails: false }

  static getDerivedStateFromError(error: Error): Partial<State> {
    return { hasError: true, error }
  }

  componentDidCatch(error: Error, errorInfo: ErrorInfo) {
    console.error('ui crash', error, errorInfo.componentStack)
    this.setState({ errorInfo })
  }

  private handleReload = () => {
    window.location.reload()
  }

  private handleHome = () => {
    window.location.assign('/')
  }

  private toggleDetails = () => {
    this.setState(s => ({ showDetails: !s.showDetails }))
  }

  render() {
    if (!this.state.hasError) return this.props.children
    const { error, errorInfo, showDetails } = this.state
    return (
      <div className="min-h-screen bg-slate-50 dark:bg-zinc-950 text-slate-900 dark:text-zinc-100 flex items-center justify-center p-4">
        <div role="alert" className="max-w-xl w-full p-6 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-100 dark:bg-zinc-900 space-y-4">
          <h1 className="text-lg font-bold tracking-tight">Something went wrong</h1>
          <p className="text-sm text-slate-600 dark:text-zinc-400">
            The page hit an unexpected error and couldn't render. You can try reloading or returning to the home page.
          </p>
          {error?.message && (
            <pre className="text-xs font-mono p-3 rounded bg-slate-50 dark:bg-black border border-slate-200 dark:border-zinc-900 overflow-auto whitespace-pre-wrap break-words">
              {error.message}
            </pre>
          )}
          <div className="flex flex-wrap gap-2">
            <button
              onClick={this.handleReload}
              className="px-3 py-1.5 bg-emerald-600 hover:bg-emerald-500 rounded text-xs font-medium text-white"
            >
              Reload
            </button>
            <button
              onClick={this.handleHome}
              className="px-3 py-1.5 bg-slate-300 dark:bg-zinc-700 hover:bg-slate-400 dark:hover:bg-zinc-600 rounded text-xs font-medium"
            >
              Go home
            </button>
            {(error?.stack || errorInfo?.componentStack) && (
              <button
                onClick={this.toggleDetails}
                className="px-3 py-1.5 bg-slate-300 dark:bg-zinc-700 hover:bg-slate-400 dark:hover:bg-zinc-600 rounded text-xs font-medium"
                aria-expanded={showDetails}
              >
                {showDetails ? 'Hide details' : 'Show details'}
              </button>
            )}
          </div>
          {showDetails && (
            <pre className="text-xs font-mono p-3 rounded bg-slate-50 dark:bg-black border border-slate-200 dark:border-zinc-900 overflow-auto max-h-[40vh] whitespace-pre-wrap break-words">
              {error?.stack}
              {errorInfo?.componentStack}
            </pre>
          )}
        </div>
      </div>
    )
  }
}
