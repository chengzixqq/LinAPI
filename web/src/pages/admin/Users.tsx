import { Button, Modal, NumberInput, TextInput, Drawer, Badge, Group, Stack, Text } from '@mantine/core'
import { useForm } from '@mantine/form'
import { useState } from 'react'
import { api } from '../../api/endpoints'
import type { APIKey, User } from '../../api/types'
import { useAsyncData } from '../../hooks/useAsyncData'
import { DataTable, TableToolbar, type ColumnDef } from '../../components/DataTable'
import { ConfirmButton } from '../../components/ConfirmButton'
import { PlaintextKeyModal } from '../../components/PlaintextKeyModal'
import { text } from '../../text'
import { notify } from '../../notify'

export default function Users() {
  const { data, loading, error, reload } = useAsyncData(() => api.admin.listUsers().then((r) => r.data))
  const [createVisible, setCreateVisible] = useState(false)
  const [creating, setCreating] = useState(false)
  const [keysUser, setKeysUser] = useState<string | null>(null) // 正在管密钥的用户。

  const form = useForm({
    initialValues: { external_id: '', balance: 0 },
    validate: { external_id: (v) => (v.trim() ? null : '请输入用户标识') },
  })

  const createUser = form.onSubmit(async (values) => {
    setCreating(true)
    try {
      await api.admin.createUser(values.external_id, values.balance ?? 0)
      notify.success('用户创建成功')
      setCreateVisible(false)
      form.reset()
      reload()
    } catch (e) {
      notify.error(e instanceof Error ? e.message : '创建失败')
    } finally {
      setCreating(false)
    }
  })

  const columns: ColumnDef<User>[] = [
    { title: '用户标识', dataIndex: 'external_id' },
    { title: '余额', dataIndex: 'balance', render: (v: number) => v.toLocaleString() },
    { title: '状态', dataIndex: 'enabled', render: (v: boolean) => <Badge color={v ? 'teal' : 'gray'}>{v ? '启用' : '禁用'}</Badge> },
    {
      title: '操作', dataIndex: 'op',
      render: (_: unknown, r: User) => (
        <Group gap="xs">
          <Button variant="subtle" size="compact-sm" onClick={() => setKeysUser(r.external_id)}>密钥</Button>
          <BalanceButton userId={r.external_id} onDone={reload} />
          {r.enabled ? (
            <ConfirmButton
              title="确认禁用该用户？" buttonText={text.common.disable} successMsg={text.common.saved}
              onConfirm={async () => { await api.admin.setUserEnabled(r.external_id, false); reload() }}
            />
          ) : (
            <Button variant="subtle" size="compact-sm" onClick={async () => {
              try { await api.admin.setUserEnabled(r.external_id, true); notify.success(text.common.saved); reload() }
              catch (e) { notify.error(e instanceof Error ? e.message : '操作失败') }
            }}>{text.common.enable}</Button>
          )}
        </Group>
      ),
    },
  ]

  return (
    <>
      <DataTable<User>
        columns={columns} data={data} loading={loading} error={error} onReload={reload} rowKey="external_id"
        toolbar={<TableToolbar title={text.nav.users} actions={<Button onClick={() => setCreateVisible(true)}>{text.common.create}</Button>} />}
      />

      <Modal opened={createVisible} onClose={() => setCreateVisible(false)} title="新建用户" centered>
        <form onSubmit={createUser}>
          <Stack gap="md">
            <TextInput label="用户标识" {...form.getInputProps('external_id')} />
            <NumberInput label="初始余额" min={0} {...form.getInputProps('balance')} />
            <Button type="submit" loading={creating}>{text.common.create}</Button>
          </Stack>
        </form>
      </Modal>

      {keysUser && <UserKeysSheet userId={keysUser} onClose={() => setKeysUser(null)} />}
    </>
  )
}

function BalanceButton({ userId, onDone }: { userId: string; onDone: () => void }) {
  const [visible, setVisible] = useState(false)
  const [loading, setLoading] = useState(false)
  const form = useForm({
    initialValues: { delta: 0 },
    validate: { delta: (v) => (v !== 0 ? null : '请输入非零金额') },
  })

  const submit = form.onSubmit(async (values) => {
    setLoading(true)
    try {
      const res = await api.admin.addBalance(userId, values.delta)
      notify.success(`充值成功，当前余额 ${res.balance.toLocaleString()}`)
      setVisible(false)
      form.reset()
      onDone()
    } catch (e) {
      notify.error(e instanceof Error ? e.message : '充值失败')
    } finally {
      setLoading(false)
    }
  })

  return (
    <>
      <Button variant="subtle" size="compact-sm" onClick={() => setVisible(true)}>充值</Button>
      <Modal opened={visible} onClose={() => setVisible(false)} title={`为 ${userId} 充值`} centered>
        <form onSubmit={submit}>
          <Stack gap="md">
            <NumberInput label="增减额（负数为扣减）" {...form.getInputProps('delta')} />
            <Button type="submit" loading={loading}>{text.common.confirm}</Button>
          </Stack>
        </form>
      </Modal>
    </>
  )
}

function UserKeysSheet({ userId, onClose }: { userId: string; onClose: () => void }) {
  const { data, loading, error, reload } = useAsyncData(() => api.admin.listKeys(userId).then((r) => r.data), [userId])
  const [plaintext, setPlaintext] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)

  const createKey = async () => {
    setCreating(true)
    try {
      const created = await api.admin.createKey(userId, 0, [])
      setPlaintext(created.api_key) // 触发明文一次性弹窗。
      reload()
    } catch (e) {
      notify.error(e instanceof Error ? e.message : '创建密钥失败')
    } finally {
      setCreating(false)
    }
  }

  const columns: ColumnDef<APIKey>[] = [
    { title: 'Key ID', dataIndex: 'key_id' },
    { title: '限流/分', dataIndex: 'rate_limit_per_min' },
    { title: '状态', dataIndex: 'enabled', render: (v: boolean) => <Badge color={v ? 'teal' : 'gray'}>{v ? '启用' : '禁用'}</Badge> },
    {
      title: '操作', dataIndex: 'op',
      render: (_: unknown, r: APIKey) => (
        r.enabled
          ? <ConfirmButton title="确认禁用该密钥？" buttonText={text.common.disable} successMsg={text.common.saved}
              onConfirm={async () => { await api.admin.setKeyEnabled(r.key_id, false); reload() }} />
          : <Button variant="subtle" size="compact-sm" onClick={async () => {
              try { await api.admin.setKeyEnabled(r.key_id, true); notify.success(text.common.saved); reload() }
              catch (e) { notify.error(e instanceof Error ? e.message : '操作失败') }
            }}>{text.common.enable}</Button>
      ),
    },
  ]

  return (
    <Drawer opened onClose={onClose} position="right" size={560} title={`${userId} 的密钥`}>
      <Stack gap="md">
        <Group>
          <Button loading={creating} onClick={createKey}>生成新密钥</Button>
          <Text c="dimmed" size="sm">明文仅在创建时显示一次，请及时保存。</Text>
        </Group>
        <DataTable<APIKey> columns={columns} data={data} loading={loading} error={error} onReload={reload} rowKey="key_id" />
      </Stack>
      <PlaintextKeyModal apiKey={plaintext ?? ''} visible={plaintext !== null} onClose={() => setPlaintext(null)} />
    </Drawer>
  )
}
