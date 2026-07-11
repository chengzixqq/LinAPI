import { Button, Modal, NumberInput, Badge, Group, Text, Stack } from '@mantine/core'
import { useForm } from '@mantine/form'
import { useState } from 'react'
import { api } from '../../api/endpoints'
import type { APIKey } from '../../api/types'
import { useAsyncData } from '../../hooks/useAsyncData'
import { DataTable, TableToolbar, type ColumnDef } from '../../components/DataTable'
import { ConfirmButton } from '../../components/ConfirmButton'
import { PlaintextKeyModal } from '../../components/PlaintextKeyModal'
import { text } from '../../text'
import { notify } from '../../notify'

export default function PortalKeys() {
  const { data, loading, error, reload } = useAsyncData(() => api.me.listKeys().then((r) => r.data))
  const [plaintext, setPlaintext] = useState<string | null>(null)
  const [formVisible, setFormVisible] = useState(false)
  const [creating, setCreating] = useState(false)

  // 自助建 key 必须带 rate_limit_per_min（后端强制 1..5000，AUD-P1-28）。
  const form = useForm({
    initialValues: { rate_limit_per_min: 60 },
    validate: {
      rate_limit_per_min: (v) => (v >= 1 && v <= 5000 ? null : '请输入 1..5000 的限流值'),
    },
  })

  const createKey = form.onSubmit(async (values) => {
    setCreating(true)
    try {
      const c = await api.me.createKey(values.rate_limit_per_min, [])
      setFormVisible(false)
      form.reset()
      setPlaintext(c.api_key)
      reload()
    } catch (e) { notify.error(e instanceof Error ? e.message : '创建失败') }
    finally { setCreating(false) }
  })

  const columns: ColumnDef<APIKey>[] = [
    { title: 'Key ID', dataIndex: 'key_id' },
    { title: '限流/分', dataIndex: 'rate_limit_per_min' },
    { title: '状态', dataIndex: 'enabled', render: (v: boolean) => <Badge color={v ? 'teal' : 'gray'}>{v ? '启用' : '禁用'}</Badge> },
    {
      title: '操作', dataIndex: 'op',
      render: (_: unknown, r: APIKey) => (
        <Group gap="xs">
          {r.enabled
            ? <ConfirmButton title="确认禁用此密钥？" buttonText={text.common.disable} successMsg={text.common.saved} type="tertiary"
                onConfirm={async () => { await api.me.setKeyEnabled(r.key_id, false); reload() }} />
            : <Button variant="subtle" size="compact-sm" onClick={async () => {
                try { await api.me.setKeyEnabled(r.key_id, true); notify.success(text.common.saved); reload() }
                catch (e) { notify.error(e instanceof Error ? e.message : '操作失败') }
              }}>{text.common.enable}</Button>}
          <ConfirmButton title="确认删除此密钥？" content="删除后使用该密钥的请求将立即失败。" buttonText={text.common.delete} successMsg={text.common.deleted}
            onConfirm={async () => { await api.me.deleteKey(r.key_id); reload() }} />
        </Group>
      ),
    },
  ]

  return (
    <>
      <DataTable<APIKey>
        columns={columns} data={data} loading={loading} error={error} onReload={reload} rowKey="key_id"
        toolbar={<TableToolbar title={text.nav.portalKeys} actions={<Button onClick={() => setFormVisible(true)}>生成新密钥</Button>} />}
      />
      <Text c="dimmed" size="sm" mt="sm">明文仅在创建时显示一次，请及时保存。</Text>

      <Modal opened={formVisible} onClose={() => setFormVisible(false)} title="生成新密钥" centered>
        <form onSubmit={createKey}>
          <Stack gap="md">
            {/* 后端强制 1..5000：0/负数会被限流层当作“不限流”，超大值绕过平台限流。 */}
            <NumberInput
              label="每分钟请求上限 (RPM)" min={1} max={5000} step={10}
              {...form.getInputProps('rate_limit_per_min')}
            />
            <Button type="submit" loading={creating}>创建</Button>
          </Stack>
        </form>
      </Modal>

      <PlaintextKeyModal apiKey={plaintext ?? ''} visible={plaintext !== null} onClose={() => setPlaintext(null)} />
    </>
  )
}
