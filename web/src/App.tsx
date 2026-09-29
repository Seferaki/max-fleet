import { useEffect, useState } from 'react'
import { Button } from '@maxhub/max-ui'
import { MapView } from './MapView'
import { getMapContext, initialSelection, returnIdFromLaunch, saveMapLocation, validPoint, MapApiError, type MapContext, type Point } from './mapApi'

declare global {
  interface Window {
    WebApp?: { initData?: string }
  }
}

function errorMessage(error: unknown): string {
  if (error instanceof MapApiError) {
    if (error.code === 'INVALID_INIT_DATA') return 'Сеанс MAX устарел. Откройте карту заново из чата.'
    if (error.code === 'NOT_FOUND') return 'Этот возврат недоступен. Откройте актуальный возврат в чате.'
    if (error.code === 'STALE_VERSION') return 'Возврат изменился. Обновите данные и выберите точку ещё раз.'
  }
  return 'Сервис временно недоступен. Если точка уже выбрана, она останется на экране для повтора.'
}

export function App() {
  const initData = window.WebApp?.initData || ''
  const returnId = returnIdFromLaunch(window.location.search, initData)
  const [context, setContext] = useState<MapContext | null>(null)
  const [selected, setSelected] = useState<Point | null>(null)
  const [landmark, setLandmark] = useState('')
  const [latitude, setLatitude] = useState('')
  const [longitude, setLongitude] = useState('')
  const [pendingKey, setPendingKey] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [tileFailed, setTileFailed] = useState(false)
  const [message, setMessage] = useState('')
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    if (!returnId || !initData) {
      setLoading(false)
      setMessage(!returnId ? 'Откройте карту из текущего возврата в чате MAX.' : 'Откройте карту внутри MAX, чтобы подтвердить доступ.')
      return
    }
    let active = true
    getMapContext(returnId, initData).then(result => {
      if (!active) return
      const point = initialSelection(result)
      setContext(result)
      setSelected(point)
      setLatitude(point ? String(point.latitude) : '')
      setLongitude(point ? String(point.longitude) : '')
      setSaved(Boolean(point))
      setLoading(false)
    }).catch(error => {
      if (!active) return
      setMessage(errorMessage(error))
      setLoading(false)
    })
    return () => { active = false }
  }, [returnId, initData])

  function choose(point: Point) {
    if (saving || !validPoint(point)) return
    setSelected(point)
    setLatitude(String(point.latitude))
    setLongitude(String(point.longitude))
    setPendingKey(crypto.randomUUID())
    setSaved(false)
    setMessage('Точка выбрана. Проверьте место и нажмите «Сохранить точку».')
  }

  function chooseCoordinates() {
    const point = { latitude: Number(latitude), longitude: Number(longitude) }
    if (latitude.trim() === '' || longitude.trim() === '' || !validPoint(point)) {
      setMessage('Введите широту от −90 до 90 и долготу от −180 до 180.')
      return
    }
    choose(point)
  }

  async function reload() {
    if (!returnId || !initData) return
    setLoading(true)
    try {
      const fresh = await getMapContext(returnId, initData)
      setContext(fresh)
      setSelected(initialSelection(fresh))
      setPendingKey(null)
      setSaved(fresh.selected)
      setMessage('Данные возврата обновлены.')
    } catch (error) {
      setMessage(errorMessage(error))
    } finally {
      setLoading(false)
    }
  }

  async function save() {
    if (!returnId || !initData || !context || !selected || !pendingKey || saving) return
    setSaving(true)
    setMessage('Сохраняем выбранную точку…')
    try {
      const result = await saveMapLocation(returnId, initData, context.return_version, {
        ...selected, landmark: landmark.trim() || undefined,
      }, pendingKey)
      setContext({ ...context, return_version: result.return_version, selected: true, selected_point: result.point })
      setPendingKey(null)
      setSaved(true)
      setMessage('Точка парковки сохранена. Вернитесь в чат MAX и продолжите возврат.')
    } catch (error) {
      setMessage(errorMessage(error))
    } finally {
      setSaving(false)
    }
  }

  return <main className="screen">
    <header className="heading">
      <span className="eyebrow">MAX Fleet · возврат автомобиля</span>
      <h1>Место парковки</h1>
      <p>Нажмите на карту или перетащите маркер. Стартовый центр карты не считается выбранной точкой.</p>
    </header>
    {loading && <p role="status">Загружаем текущий возврат…</p>}
    {message && <p className={saved ? 'notice success' : 'notice'} role="status">{message}</p>}
    {context && <>
      <section className="map-section" aria-label="Выбор точки на карте">
        <MapView center={context.initial_center} selected={selected} onSelect={choose} onTileError={() => setTileFailed(true)} />
        {tileFailed && <p className="tile-warning">Картографическая подложка не загрузилась. Вы можете ввести координаты ниже и сохранить точку.</p>}
      </section>
      <section className="form-section" aria-label="Подтверждение точки">
        <h2>{selected ? 'Выбранная точка' : 'Точка ещё не выбрана'}</h2>
        <div className="coordinates">
          <label>Широта<input type="number" inputMode="decimal" step="any" disabled={saving} value={latitude} onChange={event => setLatitude(event.target.value)} placeholder="55.751244" /></label>
          <label>Долгота<input type="number" inputMode="decimal" step="any" disabled={saving} value={longitude} onChange={event => setLongitude(event.target.value)} placeholder="37.618423" /></label>
        </div>
        <Button variant="secondary" disabled={saving} onClick={chooseCoordinates}>Выбрать введённые координаты</Button>
        <label className="landmark">Ориентир, если нужен<input maxLength={500} disabled={saving} value={landmark} onChange={event => { setLandmark(event.target.value); if (selected) { setPendingKey(crypto.randomUUID()); setSaved(false) } }} placeholder="Например, у главного входа" /></label>
        <Button variant="primary" stretched disabled={!selected || !pendingKey || saving} loading={saving} onClick={save}>Сохранить точку</Button>
        <Button variant="ghost" onClick={reload} disabled={saving || loading}>Обновить возврат</Button>
      </section>
    </>}
    <footer>Геолокация устройства не требуется. Если парковка, ключи или закрытие автомобиля вызывают сомнение, не завершайте возврат самостоятельно — свяжитесь с ответственным.</footer>
  </main>
}
