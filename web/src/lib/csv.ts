import type { CreateAccountRequest } from '@/lib/api'

/**
 * Parses a flat CSV of accounts. Rows are `localpart,password` or
 * `localpart,domain,password`; blank lines, `#` comments and a header row
 * are skipped.
 */
export function parseAccountsCsv(text: string, defaultDomain: string): CreateAccountRequest[] {
  const rows: CreateAccountRequest[] = []
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim()
    if (!line || line.startsWith('#')) continue
    const parts = line.split(',').map((p) => p.trim())
    if (parts[0].toLowerCase() === 'localpart' || parts[0].toLowerCase() === 'username') continue
    if (parts.length === 2 && parts[0] && parts[1]) rows.push({ localpart: parts[0], domain: defaultDomain, password: parts[1] })
    else if (parts.length >= 3 && parts[0] && parts[1] && parts[2]) rows.push({ localpart: parts[0], domain: parts[1], password: parts[2] })
  }
  return rows
}
