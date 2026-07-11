import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { MantineProvider } from '@mantine/core'
import { Notifications } from '@mantine/notifications'
import { ModalsProvider } from '@mantine/modals'
import App from './App'
import { AuthProvider } from './stores/auth'
import { theme } from './theme'

// Mantine 样式必须在自定义样式之前导入，保证覆盖顺序正确。
import '@mantine/core/styles.css'
import '@mantine/notifications/styles.css'
import './theme/global.css'

// defaultColorScheme="auto"：首次跟随系统，用户切换后 Mantine 自动持久化到 localStorage。
ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <MantineProvider theme={theme} defaultColorScheme="auto">
      <Notifications position="top-right" />
      <ModalsProvider>
        <AuthProvider>
          <BrowserRouter basename="/console">
            <App />
          </BrowserRouter>
        </AuthProvider>
      </ModalsProvider>
    </MantineProvider>
  </React.StrictMode>,
)
