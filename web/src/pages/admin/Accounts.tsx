import { Button, Modal, NumberInput, TextInput, PasswordInput, Select, Badge, Group, Stack } from '@mantine/core'
import { useForm } from '@mantine/form'
import { useState } from 'react'
import { api } from '../../api/endpoints'
import type { Account } from '../../api/types'
import { useAsyncData } from '../../hooks/useAsyncData'
import { DataTable, TableToolbar, type ColumnDef } from '../../components/DataTable'
import { ConfirmButton } from '../../components/ConfirmButton'
import { text } from '../../text'
import { notify } from '../../notify'

// 登录账户管理页：账户 CRUD（建号/改密/启停）。沿用 Users.tsx 建立的 CRUD 样板
// （useAsyncData 三态 + DataTable + Modal 表单 + notify + ConfirmButton 二次确认）。
export default function Accounts() {
  const { data, loading, error, reload } = useAsyncData(() => api.admin.listAccounts().then((r) => r.data))
  const [createVisible, setCreateVisible] = useState(false)
  const [creating, setCreating] = useState(false)

  // 建账户：role=user 时可带初始余额；表单不含 external_id（后端自动生成/回填）。
  const form = useForm({
    initialValues: { username: '', password: '', role: 'user', initial_balance: 0 },
    validate: {
      username: (v) => (v.trim() ? null : '请输入用户名'),
      password: (v) => (v.length >= 8 ? null : text.register.pwTooShort),
    },
  })

  const createAccount = form.onSubmit(async (values) => {
    setCreating(true)
    try {
      await api.admin.createAccount({
        username: values.username,
        password: values.password,
        role: values.role,
        // role=admin 时无此字段；仅 user 账户透传初始余额。
        ...(values.role === 'user' ? { initial_balance: values.initial_balance ?? 0 } : {}),
      })
      notify.success('账户创建成功')
      setCreateVisible(false)
      form.reset()
      reload()
    } catch (e) {
      notify.error(e instanceof Error ? e.message : '创建失败')
    } finally {
      setCreating(false)
    }
  })

  const columns: ColumnDef<Account>[] = [
    { title: 'ID', dataIndex: 'id' },
    { title: '用户名', dataIndex: 'username' },
    { title: '角色', dataIndex: 'role', render: (v: Account['role']) => <Badge color={v === 'admin' ? 'blue' : 'teal'}>{v}</Badge> },
    { title: '计费实体', dataIndex: 'external_id', render: (v?: string) => v || '—' },
    { title: '状态', dataIndex: 'enabled', render: (v: boolean) => <Badge color={v ? 'teal' : 'gray'}>{v ? '启用' : '禁用'}</Badge> },
    {
      title: '操作', dataIndex: 'op',
      render: (_: unknown, r: Account) => (
        <Group gap="xs">
          <ResetPasswordButton accountId={r.id} username={r.username} />
          {r.enabled ? (
            <ConfirmButton
              title="确认禁用该账户？" buttonText={text.common.disable} successMsg={text.common.saved}
              onConfirm={async () => { await api.admin.setAccountEnabled(r.id, false); reload() }}
            />
          ) : (
            <Button variant="subtle" size="compact-sm" onClick={async () => {
              try { await api.admin.setAccountEnabled(r.id, true); notify.success(text.common.saved); reload() }
              catch (e) { notify.error(e instanceof Error ? e.message : '操作失败') }
            }}>{text.common.enable}</Button>
          )}
        </Group>
      ),
    },
  ]

  return (
    <>
      <DataTable<Account>
        columns={columns} data={data} loading={loading} error={error} onReload={reload} rowKey="id"
        toolbar={<TableToolbar title={text.nav.accounts} actions={<Button onClick={() => setCreateVisible(true)}>{text.common.create}</Button>} />}
      />

      <Modal opened={createVisible} onClose={() => setCreateVisible(false)} title="新建账户" centered>
        <form onSubmit={createAccount}>
          <Stack gap="md">
            <TextInput label="用户名" {...form.getInputProps('username')} />
            <PasswordInput label="密码" {...form.getInputProps('password')} />
            <Select
              label="角色" data={[{ value: 'user', label: 'user' }, { value: 'admin', label: 'admin' }]}
              allowDeselect={false} {...form.getInputProps('role')}
            />
            {/* 初始余额仅对 user 账户有意义；admin 无计费实体，故切到 admin 时隐藏。 */}
            {form.values.role === 'user' && (
              <NumberInput label="初始余额" min={0} {...form.getInputProps('initial_balance')} />
            )}
            <Button type="submit" loading={creating}>{text.common.create}</Button>
          </Stack>
        </form>
      </Modal>
    </>
  )
}

// 改密：弹窗输入新密码（≥8 校验），提交 resetPassword。
function ResetPasswordButton({ accountId, username }: { accountId: number; username: string }) {
  const [visible, setVisible] = useState(false)
  const [loading, setLoading] = useState(false)
  const form = useForm({
    initialValues: { password: '' },
    validate: { password: (v) => (v.length >= 8 ? null : text.register.pwTooShort) },
  })

  const submit = form.onSubmit(async (values) => {
    setLoading(true)
    try {
      await api.admin.resetPassword(accountId, values.password)
      notify.success(text.common.saved)
      setVisible(false)
      form.reset()
    } catch (e) {
      notify.error(e instanceof Error ? e.message : '改密失败')
    } finally {
      setLoading(false)
    }
  })

  return (
    <>
      <Button variant="subtle" size="compact-sm" onClick={() => setVisible(true)}>改密</Button>
      <Modal opened={visible} onClose={() => setVisible(false)} title={`重置 ${username} 的密码`} centered>
        <form onSubmit={submit}>
          <Stack gap="md">
            <PasswordInput label="新密码" {...form.getInputProps('password')} />
            <Button type="submit" loading={loading}>{text.common.confirm}</Button>
          </Stack>
        </form>
      </Modal>
    </>
  )
}
