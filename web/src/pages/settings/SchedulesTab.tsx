import { GeneralSettings, GeneralTabProps } from './GeneralTab'

// Release filters and every timed job (search, Hardcover sync, library scan,
// discovery, series check), split out of the old General tab (#3990).
export default function SchedulesTab({ onNavigate }: GeneralTabProps = {}) {
  return <GeneralSettings sections={['schedules']} onNavigate={onNavigate} />
}
