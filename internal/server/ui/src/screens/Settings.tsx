import { useEffect, useState, type ReactNode } from 'react'
import { Bell, CheckCircle2, Database, HardDrive, MonitorCog, Moon, PlugZap, Settings as SettingsIcon, Sun } from 'lucide-react'
import { useHealth, useUiData } from '../lib/query'
import { useDocumentTitle } from '../lib/useDocumentTitle'
import { getTheme, onThemeChange, toggleTheme, type Theme } from '../lib/theme'
import { ErrorNote, Loading } from '../components/ui'
import type { ToolCapability } from '../lib/types'

type BrowserNotificationPermission = NotificationPermission | 'unsupported'

function notificationPermission(): BrowserNotificationPermission {
  if (typeof window === 'undefined' || !('Notification' in window)) return 'unsupported'
  return Notification.permission
}

export function Settings() {
  useDocumentTitle('Settings')
  const { data: ui, isLoading, error } = useUiData()
  const { data: health } = useHealth()
  const [theme, setTheme] = useState<Theme>(() => getTheme())
  const [permission, setPermission] = useState<BrowserNotificationPermission>(() => notificationPermission())

  useEffect(() => onThemeChange(setTheme), [])

  const enableNotifications = async () => {
    if (typeof window === 'undefined' || !('Notification' in window)) {
      setPermission('unsupported')
      return
    }
    setPermission(await Notification.requestPermission())
  }

  if (isLoading) return <div className="page"><Loading rows={5} /></div>
  if (error) return <div className="page"><ErrorNote error={error} /></div>

  const db = ui?.FLOWDB
  const user = ui?.USER
  const caps = ui?.CAPABILITIES

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <div className="eyebrow">system controls</div>
          <h1 className="h-xl">Settings</h1>
        </div>
      </div>

      <div className="settings-grid">
        <SettingsPanel title="Workspace" icon={<HardDrive size={17} />}>
          <KeyValue label="User" value={user?.full_name || user?.name || user?.username || 'unknown'} />
          <KeyValue label="Username" value={user?.username || 'unknown'} />
          <KeyValue label="Flow root" value={health?.flow_root || 'unknown'} />
          <KeyValue label="Version" value={health?.version || 'dev'} />
        </SettingsPanel>

        <SettingsPanel title="Database" icon={<Database size={17} />}>
          <KeyValue label="Path" value={db?.display_path || db?.path || 'unknown'} mono />
          <KeyValue label="Size" value={db?.human_size || 'unknown'} />
          <KeyValue label="Status" value={db?.exists ? 'available' : 'missing'} />
        </SettingsPanel>

        <SettingsPanel title="Preferences" icon={<SettingsIcon size={17} />}>
          <div className="setting-row">
            <div>
              <div className="setting-label">Theme</div>
              <div className="setting-value">{theme}</div>
            </div>
            <button type="button" className="btn" onClick={() => setTheme(toggleTheme())}>
              {theme === 'dark' ? <Sun size={15} /> : <Moon size={15} />}
              {theme === 'dark' ? 'Light' : 'Dark'}
            </button>
          </div>
          <div className="setting-row">
            <div>
              <div className="setting-label">Desktop alerts</div>
              <div className="setting-value">{permission}</div>
            </div>
            {permission === 'default' && (
              <button type="button" className="btn" onClick={enableNotifications}>
                <Bell size={15} /> Enable
              </button>
            )}
          </div>
        </SettingsPanel>

        <CapabilityPanel title="Agents" icon={<MonitorCog size={17} />} items={caps?.providers ?? []} />
        <CapabilityPanel title="Terminals" icon={<PlugZap size={17} />} items={caps?.terminals ?? []} />
        <CapabilityPanel title="Integrations" icon={<CheckCircle2 size={17} />} items={caps?.integrations ?? []} />
      </div>
    </div>
  )
}

function SettingsPanel({ title, icon, children }: { title: string; icon: ReactNode; children: ReactNode }) {
  return (
    <section className="settings-card">
      <div className="settings-card-head">
        <span>{icon}</span>
        <h2>{title}</h2>
      </div>
      <div className="settings-card-body">{children}</div>
    </section>
  )
}

function CapabilityPanel({ title, icon, items }: { title: string; icon: ReactNode; items: ToolCapability[] }) {
  return (
    <SettingsPanel title={title} icon={icon}>
      <div className="cap-list">
        {items.length === 0 ? (
          <div className="setting-value">none reported</div>
        ) : (
          items.map((item) => (
            <div key={item.id} className="cap-row">
              <span className={`cap-dot ${item.available ? 'on' : 'off'}`} />
              <div className="lrow-main">
                <div className="cap-title">{item.label || item.id}</div>
                <div className="cap-sub clip">{item.path || item.reason || item.status || item.id}</div>
              </div>
              <span className="tag">{item.available ? 'ready' : 'off'}</span>
            </div>
          ))
        )}
      </div>
    </SettingsPanel>
  )
}

function KeyValue({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="setting-row compact">
      <div className="setting-label">{label}</div>
      <div className={`setting-value clip${mono ? ' mono' : ''}`}>{value}</div>
    </div>
  )
}
