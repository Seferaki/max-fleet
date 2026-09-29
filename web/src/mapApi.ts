export type Point = { latitude: number; longitude: number }
export type MapContext = {
  return_id: string
  return_version: number
  selected: boolean
  selected_point: Point | null
  initial_center: Point
}
export type SavedLocation = {
  return_id: string
  return_version: number
  point: Point
  source: 'manual_map'
  selected: true
}
export type LocationDraft = Point & { landmark?: string }

const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

export class MapApiError extends Error {
  constructor(public readonly code: string, public readonly retryable: boolean) {
    super(code)
  }
}

export function returnIdFromURL(search: string): string | null {
  const params = new URLSearchParams(search)
  const ids = params.getAll('return_id')
  return ids.length === 1 && uuid.test(ids[0]) ? ids[0] : null
}

// start_param is only a routing hint. Go authenticates raw initData and verifies
// that the signed actor owns this exact current return before exposing data.
export function returnIdFromLaunch(search: string, rawInitData: string): string | null {
  const query = new URLSearchParams(search)
  const explicit = query.getAll('return_id')
  const bridge = new URLSearchParams(rawInitData).getAll('start_param')
  const launch = query.getAll('WebAppStartParam')
  if (explicit.length > 1 || bridge.length > 1 || launch.length > 1) return null
  const candidates = [explicit[0], bridge[0], launch[0]].filter((value): value is string => Boolean(value))
  if (candidates.length === 0 || candidates.some(value => !uuid.test(value) || value !== candidates[0])) return null
  return candidates[0]
}

export function validPoint(value: Point): boolean {
  return Number.isFinite(value.latitude) && Number.isFinite(value.longitude) &&
    value.latitude >= -90 && value.latitude <= 90 && value.longitude >= -180 && value.longitude <= 180
}

export function initialSelection(context: MapContext): Point | null {
  return context.selected && context.selected_point && validPoint(context.selected_point)
    ? context.selected_point : null
}

async function request<T>(path: string, initData: string, options: RequestInit): Promise<T> {
  if (!initData) throw new MapApiError('INVALID_INIT_DATA', false)
  const timeout = new AbortController()
  const timer = globalThis.setTimeout(() => timeout.abort(), 10000)
  try {
    const response = await fetch(path, {
      ...options,
      signal: timeout.signal,
      cache: 'no-store',
      credentials: 'omit',
      referrerPolicy: 'no-referrer',
      headers: { ...options.headers, Authorization: `MaxInitData ${initData}` },
    })
    const body = await response.json() as { data?: T; error?: { code: string; retryable: boolean } }
    if (!response.ok) throw new MapApiError(body.error?.code ?? 'TEMPORARY_FAILURE', body.error?.retryable ?? response.status >= 500)
    if (!body.data) throw new MapApiError('TEMPORARY_FAILURE', true)
    return body.data
  } catch (error) {
    if (error instanceof MapApiError) throw error
    throw new MapApiError('TEMPORARY_FAILURE', true)
  } finally {
    globalThis.clearTimeout(timer)
  }
}

export async function getMapContext(returnId: string, initData: string): Promise<MapContext> {
  if (!uuid.test(returnId)) throw new MapApiError('INVALID_REQUEST', false)
  const context = await request<MapContext>(`/api/v1/returns/${returnId}/context`, initData, { method: 'GET' })
  if (context.return_id !== returnId || !Number.isInteger(context.return_version) || context.return_version < 1 ||
      !validPoint(context.initial_center) || context.selected && !initialSelection(context)) {
    throw new MapApiError('TEMPORARY_FAILURE', true)
  }
  return context
}

export async function saveMapLocation(returnId: string, initData: string, version: number, point: LocationDraft, key: string): Promise<SavedLocation> {
  if (!uuid.test(returnId) || !Number.isInteger(version) || version < 1 || !validPoint(point) || key.length < 8 || key.length > 200) {
    throw new MapApiError('INVALID_REQUEST', false)
  }
  const saved = await request<SavedLocation>(`/api/v1/returns/${returnId}/location`, initData, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Idempotency-Key': key },
    body: JSON.stringify({ expected_version: version, latitude: point.latitude, longitude: point.longitude, landmark: point.landmark || undefined, confirmed: true }),
  })
  if (saved.return_id !== returnId || saved.return_version !== version + 1 || saved.source !== 'manual_map' || !saved.selected || !validPoint(saved.point) ||
      saved.point.latitude !== point.latitude || saved.point.longitude !== point.longitude) {
    throw new MapApiError('TEMPORARY_FAILURE', true)
  }
  return saved
}
