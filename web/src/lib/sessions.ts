import type { Session } from '@/lib/api'

type Translate = (key: string, options?: Record<string, unknown>) => string

/** When the session started, or was last seen for backends that only know that. */
export function sessionSince(s: Session): string {
  const stamp = s.started_at ?? s.last_seen
  return stamp ? new Date(stamp).toLocaleString() : '—'
}

/** Presence and priority for XMPP; live or idle for anything else. */
export function sessionState(s: Session, t: Translate, words: (key: string) => string): string {
  if (s.xmpp) return `${s.xmpp.status || 'online'} · ${t('backend.sessions.priority')} ${s.xmpp.priority}`
  return s.live ? words('live') : '—'
}
