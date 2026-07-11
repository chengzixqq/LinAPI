import { Button, Modal, TextInput, PasswordInput, NumberInput, Select, Badge, Group, Stack, Text, ActionIcon, Input } from '@mantine/core'
import { useForm } from '@mantine/form'
import { IconTrash, IconPlus } from '@tabler/icons-react'
import { useState } from 'react'
import { api } from '../../api/endpoints'
import type { Channel } from '../../api/types'
import { useAsyncData } from '../../hooks/useAsyncData'
import { DataTable, TableToolbar, type ColumnDef } from '../../components/DataTable'
import { ConfirmButton } from '../../components/ConfirmButton'
import { text } from '../../text'
import { notify } from '../../notify'

// 渠道管理页：渠道 CRUD。沿用 Users.tsx 的 CRUD 样板；models 用结构化键值对编辑器
// （对外模型名 → 上游模型名），提交时折叠成 Record<string,string>，避免手写 JSON 出错。
type ModelRow = { outer: string; upstream: string }

// 渠道表单值：format 保持字面量联合（否则 useForm 会推断成 string，喂给 API 报错）；
// api_key 独立字段（后端脱敏永不回填，编辑时必重填）。
type ChannelFormValues = {
  channel_id: string
  name: string
  format: 'openai' | 'anthropic'
  base_url: string
  api_key: string
  priority: number
  weight: number
}

export default function Channels() {
  const { data, loading, error, reload } = useAsyncData(() => api.admin.listChannels().then((r) => r.data))
  const [editing, setEditing] = useState<Channel | null>(null) // 正在编辑的渠道；null=新建。
  const [formVisible, setFormVisible] = useState(false)

  const openCreate = () => { setEditing(null); setFormVisible(true) }
  const openEdit = (c: Channel) => { setEditing(c); setFormVisible(true) }

  const columns: ColumnDef<Channel>[] = [
    { title: '渠道 ID', dataIndex: 'channel_id' },
    { title: '名称', dataIndex: 'name' },
    { title: '格式', dataIndex: 'format', render: (v: string) => <Badge variant="light">{v}</Badge> },
    { title: 'Base URL', dataIndex: 'base_url' },
    { title: '优先级', dataIndex: 'priority' },
    { title: '权重', dataIndex: 'weight' },
    { title: '状态', dataIndex: 'enabled', render: (v: boolean) => <Badge color={v ? 'teal' : 'gray'}>{v ? '启用' : '禁用'}</Badge> },
    {
      title: '操作', dataIndex: 'op',
      render: (_: unknown, r: Channel) => (
        <Group gap="xs">
          <Button variant="subtle" size="compact-sm" onClick={() => openEdit(r)}>{text.common.edit}</Button>
          {r.enabled ? (
            <ConfirmButton
              title="确认禁用该渠道？" buttonText={text.common.disable} successMsg={text.common.saved}
              onConfirm={async () => { await api.admin.setChannelEnabled(r.channel_id, false); reload() }}
            />
          ) : (
            <Button variant="subtle" size="compact-sm" onClick={async () => {
              try { await api.admin.setChannelEnabled(r.channel_id, true); notify.success(text.common.saved); reload() }
              catch (e) { notify.error(e instanceof Error ? e.message : '操作失败') }
            }}>{text.common.enable}</Button>
          )}
          <ConfirmButton
            title="确认删除渠道？此操作不可恢复" buttonText={text.common.delete} type="danger" successMsg={text.common.deleted}
            onConfirm={async () => { await api.admin.deleteChannel(r.channel_id); reload() }}
          />
        </Group>
      ),
    },
  ]

  return (
    <>
      <DataTable<Channel>
        columns={columns} data={data} loading={loading} error={error} onReload={reload} rowKey="channel_id"
        toolbar={<TableToolbar title={text.nav.channels} actions={<Button onClick={openCreate}>{text.common.create}</Button>} />}
      />

      {formVisible && (
        <ChannelFormModal
          channel={editing}
          onClose={() => setFormVisible(false)}
          onDone={() => { setFormVisible(false); reload() }}
        />
      )}
    </>
  )
}

// 新建/编辑共用同一表单：编辑时用 initialValues 回填、channel_id 禁改。
// api_key 列表永不返回（后端脱敏），故编辑时也必填重填（后端 PUT 需全量字段）。
function ChannelFormModal({ channel, onClose, onDone }: { channel: Channel | null; onClose: () => void; onDone: () => void }) {
  const isEdit = channel !== null
  const [saving, setSaving] = useState(false)
  // models 结构化行的本地态：编辑时把现有 models 折出初始行，新建时给一空行。
  const [rows, setRows] = useState<ModelRow[]>(() => {
    const entries = channel?.models ? Object.entries(channel.models) : []
    return entries.length > 0 ? entries.map(([outer, upstream]) => ({ outer, upstream })) : [{ outer: '', upstream: '' }]
  })
  const [modelsError, setModelsError] = useState<string | null>(null)

  const form = useForm<ChannelFormValues>({
    initialValues: channel
      ? {
          channel_id: channel.channel_id, name: channel.name, format: channel.format,
          base_url: channel.base_url, api_key: '', priority: channel.priority, weight: channel.weight,
        }
      : { channel_id: '', name: '', format: 'openai', base_url: '', api_key: '', priority: 0, weight: 1 },
    validate: {
      channel_id: (v) => (v.trim() ? null : '请输入渠道 ID'),
      name: (v) => (v.trim() ? null : '请输入名称'),
      base_url: (v) => (v.trim() ? null : '请输入 Base URL'),
      api_key: (v) => (v.trim() ? null : '请输入 API Key'),
      weight: (v) => (v >= 1 ? null : '权重需 ≥1'),
    },
  })

  const setRow = (i: number, patch: Partial<ModelRow>) => {
    setModelsError(null)
    setRows((rs) => rs.map((r, idx) => (idx === i ? { ...r, ...patch } : r)))
  }
  const addRow = () => { setModelsError(null); setRows((rs) => [...rs, { outer: '', upstream: '' }]) }
  const removeRow = (i: number) => { setModelsError(null); setRows((rs) => rs.filter((_, idx) => idx !== i)) }

  // 校验行并折叠成 models：至少一行、对外名非空且不重复；上游名留空=透传（存空串）。
  const foldModels = (): Record<string, string> | null => {
    const cleaned = rows.map((r) => ({ outer: r.outer.trim(), upstream: r.upstream.trim() }))
    if (cleaned.every((r) => r.outer === '')) { setModelsError('至少配置一个模型映射'); return null }
    const seen = new Set<string>()
    const models: Record<string, string> = {}
    for (const r of cleaned) {
      if (r.outer === '') { setModelsError('对外模型名不能为空'); return null }
      if (seen.has(r.outer)) { setModelsError(`对外模型名重复：${r.outer}`); return null }
      seen.add(r.outer)
      models[r.outer] = r.upstream
    }
    setModelsError(null)
    return models
  }

  const submit = form.onSubmit(async (values) => {
    const models = foldModels()
    if (!models) return // 就地红字已提示，阻断提交。
    setSaving(true)
    try {
      const body = {
        channel_id: values.channel_id,
        name: values.name,
        format: values.format,
        base_url: values.base_url,
        api_key: values.api_key,
        priority: values.priority,
        weight: values.weight,
        models,
      }
      if (isEdit) await api.admin.updateChannel(channel!.channel_id, body)
      else await api.admin.createChannel(body)
      notify.success(text.common.saved)
      onDone()
    } catch (e) {
      notify.error(e instanceof Error ? e.message : '保存失败')
    } finally {
      setSaving(false)
    }
  })

  return (
    <Modal opened onClose={onClose} title={isEdit ? '编辑渠道' : '新建渠道'} size="lg" centered>
      <form onSubmit={submit}>
        <Stack gap="md">
          <TextInput label="渠道 ID" disabled={isEdit} {...form.getInputProps('channel_id')} />
          <TextInput label="名称" {...form.getInputProps('name')} />
          <Select
            label="格式" data={[{ value: 'openai', label: 'openai' }, { value: 'anthropic', label: 'anthropic' }]}
            allowDeselect={false} {...form.getInputProps('format')}
          />
          <TextInput label="Base URL" {...form.getInputProps('base_url')} />
          <PasswordInput
            label="API Key"
            placeholder={isEdit ? '编辑需重新填写（后端 PUT 全量字段）' : ''}
            {...form.getInputProps('api_key')}
          />
          <NumberInput label="优先级" {...form.getInputProps('priority')} />
          <NumberInput label="权重（≥1）" min={1} {...form.getInputProps('weight')} />

          <Input.Wrapper label="模型映射（对外名 → 上游名，上游留空=透传）" error={modelsError}>
            <Stack gap="xs" mt={4}>
              {rows.map((row, i) => (
                <Group key={i} gap="xs" wrap="nowrap">
                  <TextInput placeholder="对外模型名" value={row.outer} onChange={(e) => setRow(i, { outer: e.currentTarget.value })} style={{ flex: 1 }} />
                  <Text c="dimmed">→</Text>
                  <TextInput placeholder="上游模型名（留空=透传）" value={row.upstream} onChange={(e) => setRow(i, { upstream: e.currentTarget.value })} style={{ flex: 1 }} />
                  <ActionIcon variant="subtle" color="red" onClick={() => removeRow(i)} disabled={rows.length === 1} aria-label="删除此行">
                    <IconTrash size={16} />
                  </ActionIcon>
                </Group>
              ))}
              <Button variant="subtle" size="compact-sm" leftSection={<IconPlus size={16} />} onClick={addRow} w="fit-content">
                添加模型
              </Button>
            </Stack>
          </Input.Wrapper>

          <Button type="submit" loading={saving}>{text.common.save}</Button>
        </Stack>
      </form>
    </Modal>
  )
}
