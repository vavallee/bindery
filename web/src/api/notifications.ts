import { request } from './core'

export interface NotificationConfig {
  id: number
  name: string
  type: string
  url: string
  method: string
  headers: string
  onGrab: boolean
  onImport: boolean
  onUpgrade: boolean
  onFailure: boolean
  onHealth: boolean
  enabled: boolean
}

export const notificationsApi = {
  // Notifications
  listNotifications: () => request<NotificationConfig[]>('/notification'),
  addNotification: (data: Partial<NotificationConfig>) => request<NotificationConfig>('/notification', { method: 'POST', body: JSON.stringify(data) }),
  updateNotification: (id: number, data: Partial<NotificationConfig>) => request<NotificationConfig>(`/notification/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteNotification: (id: number) => request<void>(`/notification/${id}`, { method: 'DELETE' }),
  testNotification: (id: number) => request<{ message: string }>(`/notification/${id}/test`, { method: 'POST' }),
}
