// The old General tab was one long scroll (#3990). It is now four tabs that
// share the GeneralSettings component, each showing only its own sections:
//   general   Appearance, Security (every signed in user)
//   naming    File Naming: import mode, content checks, drop and source folders, templates
//   storage   Storage, Database check, Library scan and reorganize, Default library location
//   schedules Downloads filters, search interval, Hardcover sync, library scan interval,
//             release discovery, series check
// Every setting key, anchor id and save path is the one the single tab had.
export type GeneralSection = 'general' | 'naming' | 'storage' | 'schedules'
export const GENERAL_SECTIONS: GeneralSection[] = ['general', 'naming', 'storage', 'schedules']
