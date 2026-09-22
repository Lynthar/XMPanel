import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import clsx from 'clsx'
import {
  serversApi, listErrorMessage, sampleRanges,
  type Check, type CheckItem, type Point, type SampleRange, type Series, type Server,
} from '@/lib/api'
import TimeSeriesChart, { type ChartSeries } from '@/components/TimeSeriesChart'

/** Overview tab: what the background monitor has recorded about this server. */
export default function Overview({ server }: { server: Server }) {
  const { t } = useTranslation()
  const [range, setRange] = useState<SampleRange>('24h')

  const samples = useQuery({
    queryKey: ['server-samples', server.id, range],
    queryFn: async () => (await serversApi.samples(server.id, range)).data as Series,
    refetchInterval: 60_000,
  })
  const checks = useQuery({
    queryKey: ['server-checks', server.id],
    queryFn: async () => (await serversApi.checks(server.id)).data as Check[],
  })

  const counts: ChartSeries[] = [
    { key: 'online_users', label: t('servers.stats.onlineUsers'), color: '#60a5fa', value: (p) => p.online_users },
    { key: 'registered_users', label: t('servers.stats.registeredUsers'), color: '#a78bfa', value: (p) => p.registered_users },
    { key: 'active_sessions', label: t(`backend.sessions.${server.protocol}.stat`), color: '#34d399', value: (p) => p.active_sessions },
    { key: 'rooms', label: t('servers.stats.rooms'), color: '#fbbf24', value: (p) => p.rooms },
  ]

  return (
    <div className="space-y-6">
      <div className="flex gap-1">
        {sampleRanges.map((option) => (
          <button
            key={option}
            onClick={() => setRange(option)}
            className={clsx(
              'px-3 py-1 text-sm rounded',
              range === option ? 'bg-primary-600 text-white' : 'text-gray-400 hover:text-white hover:bg-gray-700'
            )}
          >
            {t(`monitor.ranges.${option}`)}
          </button>
        ))}
      </div>

      {samples.isError ? (
        <p className="text-center text-gray-400 py-8">{listErrorMessage(samples.error, t)}</p>
      ) : (
        <>
          <section className="card">
            <h3 className="text-sm font-medium text-gray-300 mb-3">{t('monitor.availability')}</h3>
            <Availability points={samples.data?.points ?? []} emptyLabel={t('monitor.noSamples')} />
            <h3 className="text-sm font-medium text-gray-300 mt-6 mb-1">{t('monitor.latency')}</h3>
            <TimeSeriesChart
              points={samples.data?.points ?? []}
              from={samples.data?.from ?? ''}
              to={samples.data?.to ?? ''}
              series={[{ key: 'latency', label: t('monitor.latency'), color: '#60a5fa', value: (p) => p.latency_ms }]}
              format={(value) => `${Math.round(value)} ms`}
              emptyLabel={t('monitor.noSamples')}
            />
          </section>

          <section className="card">
            <h3 className="text-sm font-medium text-gray-300 mb-1">{t('monitor.counts')}</h3>
            <TimeSeriesChart
              points={samples.data?.points ?? []}
              from={samples.data?.from ?? ''}
              to={samples.data?.to ?? ''}
              series={counts}
              emptyLabel={t('monitor.noSamples')}
            />
          </section>
        </>
      )}

      <section className="card">
        <h3 className="text-sm font-medium text-gray-300 mb-3">{t('monitor.checks')}</h3>
        {checks.isError ? (
          <p className="text-center text-gray-400 py-4">{listErrorMessage(checks.error, t)}</p>
        ) : checks.data && checks.data.length > 0 ? (
          <div className="space-y-4">
            {checks.data.map((check) => <CheckCard key={check.kind} check={check} />)}
          </div>
        ) : (
          <p className="text-center text-gray-500 py-4 text-sm">{t('monitor.noChecks')}</p>
        )}
      </section>
    </div>
  )
}

/**
 * One bar per bucket, coloured by the share of probes that reached the server.
 * It sits above the latency chart on the same window, so an outage lines up
 * with the gap in the line.
 */
function Availability({ points, emptyLabel }: { points: Point[]; emptyLabel: string }) {
  if (points.length === 0) {
    return <p className="text-gray-500 text-sm py-2">{emptyLabel}</p>
  }
  return (
    <div className="flex gap-px h-8 items-stretch">
      {points.map((point) => (
        <div
          key={point.ts}
          title={`${new Date(point.ts).toLocaleString()} · ${Math.round(point.ok_ratio * 100)}%`}
          className={clsx(
            'flex-1 rounded-sm min-w-px',
            point.ok_ratio >= 1 ? 'bg-green-500' : point.ok_ratio > 0 ? 'bg-yellow-500' : 'bg-red-500'
          )}
        />
      ))}
    </div>
  )
}

function CheckCard({ check }: { check: Check }) {
  const { t } = useTranslation()
  return (
    <div>
      <div className="flex items-center gap-2 mb-1">
        <StatusBadge status={check.status} />
        <span className="text-white text-sm">{t(`monitor.kinds.${check.kind}`, check.kind)}</span>
        <span className="text-gray-500 text-xs ml-auto">{new Date(check.ts).toLocaleString()}</span>
      </div>
      <table className="w-full text-sm">
        <tbody>
          {check.detail.items.map((item) => <CheckRow key={item.target} item={item} />)}
        </tbody>
      </table>
    </div>
  )
}

function CheckRow({ item }: { item: CheckItem }) {
  const { t } = useTranslation()
  return (
    <tr className="border-t border-gray-800">
      <td className="py-1 pr-4 align-top w-6"><StatusDot status={item.status} /></td>
      <td className="py-1 pr-4 align-top font-mono text-gray-300 break-all">{item.target}</td>
      <td className="py-1 align-top text-gray-400 break-all">
        {item.values && item.values.length > 0 && <span>{item.values.join(', ')}</span>}
        {item.not_after && (
          <span className="ml-2 text-gray-500">
            {t('monitor.expires', { date: new Date(item.not_after).toLocaleString() })}
          </span>
        )}
        {item.error && <span className="block text-red-400">{item.error}</span>}
        {!item.values?.length && !item.not_after && !item.error && <span className="text-gray-600">{t('monitor.notPublished')}</span>}
      </td>
    </tr>
  )
}

const statusColor = { ok: 'bg-green-500', warn: 'bg-yellow-500', fail: 'bg-red-500' } as const
const statusBadge = { ok: 'badge-green', warn: 'badge-yellow', fail: 'badge-red' } as const

function StatusDot({ status }: { status: CheckItem['status'] }) {
  return <span className={clsx('inline-block w-2 h-2 rounded-full mt-1.5', statusColor[status])} />
}

function StatusBadge({ status }: { status: Check['status'] }) {
  const { t } = useTranslation()
  return <span className={clsx('badge', statusBadge[status])}>{t(`monitor.status.${status}`)}</span>
}
