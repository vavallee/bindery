import { GeneralSettings, GeneralTabProps } from './GeneralTab'

// File naming and import behaviour, split out of the old General tab (#3990).
export default function NamingTab({ onNavigate }: GeneralTabProps = {}) {
  return <GeneralSettings sections={['naming']} onNavigate={onNavigate} />
}
