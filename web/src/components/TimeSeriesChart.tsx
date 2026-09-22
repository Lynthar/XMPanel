import { useMemo, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react'
import type { Point } from '@/lib/api'

export interface ChartSeries {
  key: string
  label: string
  color: string
  /** Reads the value this series plots; null leaves a gap in the line. */
  value: (point: Point) => number | null
}

interface Props {
  points: Point[]
  from: string
  to: string
  series: ChartSeries[]
  /** Formats a value for the axis and the tooltip. */
  format?: (value: number) => string
  emptyLabel: string
  /** Height of the drawing space; with the fixed width it sets the aspect ratio. */
  height?: number
}

// The chart is drawn in its own coordinate space and scaled to the container
// by the viewBox, so nothing has to be measured in the browser.
const VIEW_WIDTH = 900
const PADDING = { top: 12, right: 12, bottom: 22, left: 48 }

/**
 * A line chart over a fixed time window. Every series shares one y axis, so
 * plot values of the same unit together — latency in one chart, counts in
 * another.
 */
export default function TimeSeriesChart({ points, from, to, series, format, emptyLabel, height = 200 }: Props) {
  const svgRef = useRef<SVGSVGElement>(null)
  const [hover, setHover] = useState<number | null>(null)

  const start = Date.parse(from)
  const end = Date.parse(to)
  const plot = useMemo(() => {
    const values = points.flatMap((point) => series.map((s) => s.value(point))).filter((v): v is number => v !== null)
    // A flat line at zero still needs a visible axis, hence the minimum span.
    const max = Math.max(1, ...values)
    return { max, width: VIEW_WIDTH - PADDING.left - PADDING.right, height: height - PADDING.top - PADDING.bottom }
  }, [points, series, height])

  const x = (ts: string) => PADDING.left + ((Date.parse(ts) - start) / (end - start)) * plot.width
  const y = (value: number) => PADDING.top + plot.height - (value / plot.max) * plot.height

  if (points.length === 0) {
    return <div className="flex items-center justify-center h-40 text-gray-500 text-sm">{emptyLabel}</div>
  }

  const ticks = [0, 0.5, 1].map((fraction) => ({ value: plot.max * fraction, y: y(plot.max * fraction) }))
  const times = [0, 0.25, 0.5, 0.75, 1].map((fraction) => ({
    at: new Date(start + (end - start) * fraction),
    x: PADDING.left + plot.width * fraction,
  }))

  // The nearest point to the pointer wins, so a sparse series still reacts
  // across the whole width.
  const track = (event: ReactPointerEvent<SVGSVGElement>) => {
    const svg = svgRef.current
    if (!svg) return
    const box = svg.getBoundingClientRect()
    const at = start + ((event.clientX - box.left) / box.width) * (end - start)
    let nearest = 0
    for (let i = 1; i < points.length; i++) {
      if (Math.abs(Date.parse(points[i].ts) - at) < Math.abs(Date.parse(points[nearest].ts) - at)) nearest = i
    }
    setHover(nearest)
  }

  const hovered = hover !== null ? points[hover] : null

  return (
    <div className="relative">
      <svg
        ref={svgRef}
        viewBox={`0 0 ${VIEW_WIDTH} ${height}`}
        className="w-full"
        onPointerMove={track}
        onPointerLeave={() => setHover(null)}
      >
        {ticks.map((tick) => (
          <g key={tick.value}>
            <line x1={PADDING.left} x2={VIEW_WIDTH - PADDING.right} y1={tick.y} y2={tick.y} stroke="#374151" strokeWidth={1} />
            <text x={PADDING.left - 6} y={tick.y + 4} textAnchor="end" fill="#9ca3af" fontSize={11}>
              {format ? format(tick.value) : Math.round(tick.value)}
            </text>
          </g>
        ))}
        {times.map((time) => (
          <text key={time.at.toISOString()} x={time.x} y={height - 6} textAnchor="middle" fill="#9ca3af" fontSize={11}>
            {time.at.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
          </text>
        ))}

        {series.map((s) => (
          <g key={s.key}>
            {segments(points, s.value).map((segment, index) => (
              <polyline
                key={index}
                points={segment.map((p) => `${x(p.ts)},${y(s.value(p) as number)}`).join(' ')}
                fill="none"
                stroke={s.color}
                strokeWidth={2}
                strokeLinecap="round"
                strokeLinejoin="round"
              />
            ))}
          </g>
        ))}

        {hovered && (
          <line x1={x(hovered.ts)} x2={x(hovered.ts)} y1={PADDING.top} y2={PADDING.top + plot.height} stroke="#6b7280" strokeWidth={1} />
        )}
      </svg>

      {hovered && (
        <div className="absolute top-0 right-0 bg-gray-900 border border-gray-700 rounded px-2 py-1 text-xs pointer-events-none">
          <div className="text-gray-400">{new Date(hovered.ts).toLocaleString()}</div>
          {series.map((s) => {
            const value = s.value(hovered)
            return (
              <div key={s.key} className="flex items-center gap-2">
                <span className="w-2 h-2 rounded-full" style={{ backgroundColor: s.color }} />
                <span className="text-gray-300">{s.label}</span>
                <span className="text-white ml-auto">{value === null ? '—' : format ? format(value) : value}</span>
              </div>
            )
          })}
        </div>
      )}

      <div className="flex flex-wrap gap-3 mt-2 text-xs text-gray-400">
        {series.map((s) => (
          <span key={s.key} className="flex items-center gap-1.5">
            <span className="w-3 h-0.5 rounded" style={{ backgroundColor: s.color }} />
            {s.label}
          </span>
        ))}
      </div>
    </div>
  )
}

/**
 * Splits the points into runs the series can actually plot. A bucket with no
 * value breaks the line instead of being drawn through, so an outage is not
 * hidden by a straight segment across it.
 */
function segments(points: Point[], value: (point: Point) => number | null): Point[][] {
  const runs: Point[][] = []
  let current: Point[] = []
  for (const point of points) {
    if (value(point) === null) {
      if (current.length > 0) runs.push(current)
      current = []
      continue
    }
    current.push(point)
  }
  if (current.length > 0) runs.push(current)
  // A lone point has no line to draw; give it a two-point run so it shows.
  return runs.map((run) => (run.length === 1 ? [run[0], run[0]] : run))
}
