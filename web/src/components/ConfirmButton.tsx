import { Button } from '@mantine/core'
import { modals } from '@mantine/modals'
import { useState, type ReactNode } from 'react'
import { text } from '../text'
import { notify } from '../notify'

interface Props {
  onConfirm: () => Promise<void>
  title: string
  content?: ReactNode
  buttonText: string
  // danger=红色危险操作；tertiary=弱化次要操作；primary=常规。
  type?: 'danger' | 'primary' | 'tertiary'
  successMsg?: string
}

// ConfirmButton 封装「危险操作二次确认 + loading 态 + 结果反馈」，
// 落实四硬指标之②（反馈即时 + 危险操作二次确认）。
// 用 @mantine/modals 的 openConfirmModal 弹确认框，确认后执行 onConfirm。
export function ConfirmButton({ onConfirm, title, content, buttonText, type = 'danger', successMsg }: Props) {
  const [loading, setLoading] = useState(false)

  const run = async () => {
    setLoading(true)
    try {
      await onConfirm()
      if (successMsg) notify.success(successMsg)
    } catch (e) {
      notify.error(e instanceof Error ? e.message : '操作失败')
    } finally {
      setLoading(false)
    }
  }

  const open = () =>
    modals.openConfirmModal({
      title,
      children: content ?? null,
      labels: { confirm: text.common.confirm, cancel: text.common.cancel },
      confirmProps: { color: type === 'danger' ? 'red' : 'violet' },
      onConfirm: () => { void run() },
    })

  // 按钮观感：danger=红填充轻量、tertiary=灰 subtle、primary=主色 subtle。
  const color = type === 'danger' ? 'red' : type === 'tertiary' ? 'gray' : 'violet'
  return (
    <Button variant="subtle" color={color} size="compact-sm" loading={loading} onClick={open}>
      {buttonText}
    </Button>
  )
}
