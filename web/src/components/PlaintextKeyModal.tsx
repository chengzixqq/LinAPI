import { Modal, Alert, Button, Code, Group, Stack } from '@mantine/core'
import { useClipboard } from '@mantine/hooks'
import { IconAlertTriangle, IconCopy, IconCheck } from '@tabler/icons-react'
import { text } from '../text'
import { notify } from '../notify'

// PlaintextKeyModal 落实「明文仅回显一次」硬约束：大字明文 + 一键复制 + 强警告。
export function PlaintextKeyModal({ apiKey, visible, onClose }: { apiKey: string; visible: boolean; onClose: () => void }) {
  const clipboard = useClipboard({ timeout: 2000 })

  const copy = () => {
    clipboard.copy(apiKey)
    if (clipboard.error) notify.error('复制失败，请手动选择复制')
    else notify.success(text.key.copied)
  }

  return (
    <Modal
      opened={visible}
      onClose={onClose}
      title={text.key.plaintextTitle}
      closeOnClickOutside={false}
      centered
      size="lg"
    >
      <Stack gap="md">
        <Alert color="orange" icon={<IconAlertTriangle size={18} />}>
          {text.key.plaintextWarn}
        </Alert>
        <Code block style={{ fontSize: 15, wordBreak: 'break-all', whiteSpace: 'pre-wrap' }}>
          {apiKey}
        </Code>
        <Group justify="space-between">
          <Button
            leftSection={clipboard.copied ? <IconCheck size={16} /> : <IconCopy size={16} />}
            onClick={copy}
          >
            {clipboard.copied ? text.key.copied : text.key.copy}
          </Button>
          <Button variant="default" onClick={onClose}>{text.common.confirm}</Button>
        </Group>
      </Stack>
    </Modal>
  )
}
