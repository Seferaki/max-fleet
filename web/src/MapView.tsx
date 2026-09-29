import { useEffect, useRef } from 'react'
import L from 'leaflet'
import 'leaflet/dist/leaflet.css'
import type { Point } from './mapApi'

type Props = {
  center: Point
  selected: Point | null
  onSelect: (point: Point) => void
  onTileError: () => void
}

const tileURL = import.meta.env.VITE_MAP_TILE_URL || 'https://tile.openstreetmap.org/{z}/{x}/{y}.png'
const attribution = import.meta.env.VITE_MAP_ATTRIBUTION || '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors'

export function MapView({ center, selected, onSelect, onTileError }: Props) {
  const element = useRef<HTMLDivElement>(null)
  const map = useRef<L.Map | null>(null)
  const marker = useRef<L.Marker | null>(null)
  const select = useRef(onSelect)
  const tileError = useRef(onTileError)
  select.current = onSelect
  tileError.current = onTileError

  useEffect(() => {
    if (!element.current) return
    const instance = L.map(element.current, { zoomControl: true }).setView([center.latitude, center.longitude], 16)
    map.current = instance
    const layer = L.tileLayer(tileURL, { attribution, maxZoom: 19 }).addTo(instance)
    layer.on('tileerror', () => tileError.current())
    instance.on('click', event => select.current({ latitude: event.latlng.lat, longitude: event.latlng.lng }))
    return () => {
      marker.current = null
      map.current = null
      instance.remove()
    }
  }, [center.latitude, center.longitude])

  useEffect(() => {
    if (!map.current) return
    if (!selected) {
      marker.current?.remove()
      marker.current = null
      return
    }
    const position: L.LatLngExpression = [selected.latitude, selected.longitude]
    if (!marker.current) {
      const next = L.marker(position, { draggable: true }).addTo(map.current)
      next.on('dragend', () => {
        const point = next.getLatLng()
        select.current({ latitude: point.lat, longitude: point.lng })
      })
      marker.current = next
    } else {
      marker.current.setLatLng(position)
    }
  }, [selected])

  return <div ref={element} className="map-canvas" role="application" aria-label="Карта для ручного выбора парковки" />
}
