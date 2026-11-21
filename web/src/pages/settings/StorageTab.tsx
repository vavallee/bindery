import { GeneralSettings, GeneralTabProps } from './GeneralTab'

// Storage, database check, library scan and the default library location,
// split out of the old General tab (#3990).
export default function StorageTab({ onNavigate }: GeneralTabProps = {}) {
  return <GeneralSettings sections={['storage']} onNavigate={onNavigate} />
}
