import { Switch, Button, Center, Loader, Stack, Text, Title } from '@mantine/core'
import { useForm } from '@mantine/form'
import { IconAlertCircle } from '@tabler/icons-react'
import { useState } from 'react'
import { api } from '../../api/endpoints'
import type { Settings as SettingsT } from '../../api/types'
import { useAsyncData } from '../../hooks/useAsyncData'
import { text } from '../../text'
import { notify } from '../../notify'

export default function Settings() {
  const { data, loading, error, reload } = useAsyncData(() => api.admin.getSettings())
  const [saving, setSaving] = useState(false)

  if (loading) return <Center mih={300}><Loader size="lg" /></Center>
  if (error || !data) return (
    <Center mih={300}>
      <Stack align="center" gap="xs">
        <IconAlertCircle size={48} color="var(--mantine-color-red-6)" />
        <Text c="dimmed">{text.common.loadError}{error ? `：${error}` : ''}</Text>
        <Button onClick={reload} mt="sm">{text.common.retry}</Button>
      </Stack>
    </Center>
  )

  return <SettingsForm data={data} saving={saving} setSaving={setSaving} />
}

// 表单拆成独立组件：确保拿到 data 后再以其为 initialValues 初始化 useForm（hook 不能条件调用）。
function SettingsForm({ data, saving, setSaving }: { data: SettingsT; saving: boolean; setSaving: (b: boolean) => void }) {
  const form = useForm<SettingsT>({ initialValues: data })

  const save = form.onSubmit(async (values) => {
    setSaving(true)
    try { await api.admin.putSettings(values); notify.success(text.common.saved) }
    catch (e) { notify.error(e instanceof Error ? e.message : '保存失败') }
    finally { setSaving(false) }
  })

  return (
    <>
      <Title order={4} mb="md">{text.nav.settings}</Title>
      <form onSubmit={save} style={{ maxWidth: 480 }}>
        <Stack gap="md">
          <Switch label="开放注册" {...form.getInputProps('registration_enabled', { type: 'checkbox' })} />
          {/* new_user_initial_balance 已废弃（AUD-P0-07）：注册恒不发额度，后端拒绝非 0 值，故不提供输入框。 */}
          <Text c="dimmed" size="sm">
            自助注册不发放额度；如需给用户额度，请到「用户管理」建号或充值。
          </Text>
          <Button type="submit" loading={saving} w="fit-content">{text.common.save}</Button>
        </Stack>
      </form>
    </>
  )
}
