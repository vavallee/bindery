import { GeneralSettings } from '../pages/settings/GeneralTab'
import { GENERAL_SECTIONS } from '../pages/settings/generalSections'

// The General settings tab was split into four tabs (#3990). Tests that cover
// behaviour of the shared component and do not care which tab a control lives
// on mount every section at once, which is what the single tab used to render.
export default function AllGeneralSections() {
  return <GeneralSettings sections={GENERAL_SECTIONS} />
}
