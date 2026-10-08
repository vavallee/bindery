import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import { i18nReady } from './i18n'
import App from './App'
import ErrorBoundary from './components/ErrorBoundary'

// Non-English locales load lazily, so wait for the active one before the
// first render rather than painting raw keys. For English this settles
// without a fetch. A failed load still renders, in the English fallback.
void i18nReady
  .catch(() => undefined)
  .then(() => {
    createRoot(document.getElementById('root')!).render(
      <StrictMode>
        <ErrorBoundary>
          <App />
        </ErrorBoundary>
      </StrictMode>,
    )
  })
