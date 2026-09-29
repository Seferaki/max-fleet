// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { App } from './App'

vi.mock('./MapView', () => ({
  MapView: ({ onSelect, onTileError }: { onSelect: (point: { latitude: number; longitude: number }) => void; onTileError: () => void }) =>
    <div>
      <button onClick={() => onSelect({ latitude: 55.8, longitude: 37.8 })}>Тест: выбрать на карте</button>
      <button onClick={onTileError}>Тест: ошибка тайлов</button>
    </div>,
}))

const id = '10000000-0000-4000-8000-000000000001'

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  window.WebApp = undefined
  window.history.replaceState(null, '', '/')
})

it('keeps manual point and key after timeout; works when tiles and GPS fail', async () => {
  window.history.replaceState(null, '', `/?return_id=${id}`)
  window.WebApp = { initData: 'synthetic-signed-data' }
  const calls: Array<{ path: string; init: RequestInit }> = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, init: RequestInit) => {
    calls.push({ path, init })
    if (calls.length === 1) return new Response(JSON.stringify({ data: {
      return_id: id, return_version: 2, selected: false, selected_point: null,
      initial_center: { latitude: 55.7, longitude: 37.6 },
    } }), { status: 200 })
    if (calls.length === 2) throw new Error('network timeout')
    return new Response(JSON.stringify({ data: {
      return_id: id, return_version: 3, point: { latitude: 55.8, longitude: 37.8 }, source: 'manual_map', selected: true,
    } }), { status: 200 })
  }))
  render(<App />)
  const save = await screen.findByRole('button', { name: 'Сохранить точку' })
  expect((save as HTMLButtonElement).disabled).toBe(true)
  fireEvent.click(screen.getByRole('button', { name: 'Тест: ошибка тайлов' }))
  expect(screen.getByText(/подложка не загрузилась/)).toBeTruthy()
  fireEvent.change(screen.getByLabelText('Широта'), { target: { value: '55.9' } })
  fireEvent.change(screen.getByLabelText('Долгота'), { target: { value: '37.9' } })
  fireEvent.click(screen.getByRole('button', { name: 'Выбрать введённые координаты' }))
  expect((save as HTMLButtonElement).disabled).toBe(false)
  fireEvent.click(screen.getByRole('button', { name: 'Тест: выбрать на карте' }))
  expect((save as HTMLButtonElement).disabled).toBe(false)
  fireEvent.click(save)
  await screen.findByText(/она останется на экране для повтора/)
  expect((save as HTMLButtonElement).disabled).toBe(false)
  fireEvent.click(save)
  await screen.findByText(/Точка парковки сохранена/)
  expect(calls).toHaveLength(3)
  expect((calls[1].init.headers as Record<string, string>)['Idempotency-Key']).toBe((calls[2].init.headers as Record<string, string>)['Idempotency-Key'])
  expect(calls[1].path).toBe(`/api/v1/returns/${id}/location`)
  expect(window.location.search).toBe(`?return_id=${id}`)
  expect(navigator.geolocation).toBeUndefined()
})

it('restores a saved marker on reopen without resubmitting', async () => {
  window.history.replaceState(null, '', `/?return_id=${id}`)
  window.WebApp = { initData: 'synthetic-signed-data' }
  const fetcher = vi.fn(async () => new Response(JSON.stringify({ data: {
    return_id: id, return_version: 3, selected: true, selected_point: { latitude: 55.8, longitude: 37.8 },
    initial_center: { latitude: 55.8, longitude: 37.8 },
  } }), { status: 200 }))
  vi.stubGlobal('fetch', fetcher)
  render(<App />)
  const save = await screen.findByRole('button', { name: 'Сохранить точку' })
  await waitFor(() => expect(screen.getByText('Выбранная точка')).toBeTruthy())
  expect((save as HTMLButtonElement).disabled).toBe(true)
  expect((screen.getByLabelText('Широта') as HTMLInputElement).value).toBe('55.8')
  expect(fetcher).toHaveBeenCalledTimes(1)
})

it('does not call API outside a MAX Bridge session', async () => {
  window.history.replaceState(null, '', `/?return_id=${id}`)
  const fetcher = vi.fn()
  vi.stubGlobal('fetch', fetcher)
  render(<App />)
  expect(await screen.findByText(/Откройте карту внутри MAX/)).toBeTruthy()
  expect(fetcher).not.toHaveBeenCalled()
})
