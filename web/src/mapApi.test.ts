import { afterEach, describe, expect, it, vi } from 'vitest'
import { getMapContext, initialSelection, returnIdFromURL, saveMapLocation, type MapContext } from './mapApi'

const id = '10000000-0000-4000-8000-000000000001'
const context: MapContext = {
  return_id: id, return_version: 2, selected: false, selected_point: null,
  initial_center: { latitude: 55.7, longitude: 37.6 },
}

afterEach(() => vi.unstubAllGlobals())

describe('manual map data', () => {
  it('does not treat the map center as a selected parking point', () => {
    expect(initialSelection(context)).toBeNull()
    expect(initialSelection({ ...context, selected: true, selected_point: { latitude: 55.8, longitude: 37.8 } })).toEqual({ latitude: 55.8, longitude: 37.8 })
    expect(returnIdFromURL(`?return_id=${id}`)).toBe(id)
    expect(returnIdFromURL(`?return_id=${id}&return_id=${id}`)).toBeNull()
  })

  it('sends raw initData only in the header and retains the caller key on retry', async () => {
    const calls: Array<{ path: string; init: RequestInit }> = []
    vi.stubGlobal('fetch', vi.fn(async (path: string, init: RequestInit) => {
      calls.push({ path, init })
      if (calls.length === 1) throw new Error('lost response')
      return new Response(JSON.stringify({ data: { return_id: id, return_version: 3, point: { latitude: 55.8, longitude: 37.8 }, source: 'manual_map', selected: true } }), { status: 200 })
    }))
    const point = { latitude: 55.8, longitude: 37.8 }
    await expect(saveMapLocation(id, 'signed-raw-init-data', 2, point, 'stable-key-1')).rejects.toMatchObject({ code: 'TEMPORARY_FAILURE', retryable: true })
    await expect(saveMapLocation(id, 'signed-raw-init-data', 2, point, 'stable-key-1')).resolves.toMatchObject({ return_version: 3 })
    expect(calls).toHaveLength(2)
    expect(calls[0].path).toBe(`/api/v1/returns/${id}/location`)
    for (const { init } of calls) {
      expect(init.headers).toMatchObject({ Authorization: 'MaxInitData signed-raw-init-data', 'Idempotency-Key': 'stable-key-1' })
      expect(JSON.parse(init.body as string)).toMatchObject({ confirmed: true, expected_version: 2, ...point })
    }
  })

  it('reads an unselected context and rejects a forged or missing Bridge session', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ data: context }), { status: 200 })))
    await expect(getMapContext(id, '')).rejects.toMatchObject({ code: 'INVALID_INIT_DATA' })
    await expect(getMapContext(id, 'synthetic')).resolves.toMatchObject({ selected: false })
  })
})
