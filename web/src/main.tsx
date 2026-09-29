import React from 'react'
import { createRoot } from 'react-dom/client'
import { MaxUI } from '@maxhub/max-ui'
import '@maxhub/max-ui/dist/styles.css'
import { App } from './App'
import './styles.css'

const root = document.getElementById('root')
if (!root) throw new Error('Корневой элемент отсутствует')

createRoot(root).render(
  <React.StrictMode>
    <MaxUI><App /></MaxUI>
  </React.StrictMode>,
)
