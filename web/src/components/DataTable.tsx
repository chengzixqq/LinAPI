import { Table, Skeleton, Button, Group, Title, Center, Stack, Text, Pagination } from '@mantine/core'
import { IconMoodEmpty, IconAlertCircle } from '@tabler/icons-react'
import { useState, type ReactNode } from 'react'
import { text } from '../text'

// ColumnDef 保留 Semi Table 的列抽象（title/dataIndex/render），让页面层零改动迁移：
//   - dataIndex 是取值键，也用作 React key；'op' 等无实际字段的列靠 render 产出。
//   - render(value, record) 优先；无 render 时直接渲染 record[dataIndex]。
export interface ColumnDef<T> {
  title: string
  dataIndex: string
  render?: (value: any, record: T) => ReactNode
}

interface Props<T extends Record<string, any>> {
  columns: ColumnDef<T>[]
  data: T[] | null
  loading: boolean
  error: string | null
  onReload: () => void
  rowKey: string
  toolbar?: ReactNode
  emptyText?: string
  pageSize?: number
}

const DEFAULT_PAGE_SIZE = 10

// DataTable 统一列表四态：加载→骨架屏；错误→错误态+重试；空→空态；有数据→表格 + 客户端分页。
// Mantine 无内置分页表格，故这里用 Table 基元 + 本地 page 切片自实现，行为对齐原 Semi 版。
export function DataTable<T extends Record<string, any>>({
  columns, data, loading, error, onReload, rowKey, toolbar, emptyText, pageSize = DEFAULT_PAGE_SIZE,
}: Props<T>) {
  const [page, setPage] = useState(1)

  const renderToolbar = toolbar && <div style={{ marginBottom: 16 }}>{toolbar}</div>

  if (loading) {
    return (
      <div>
        {renderToolbar}
        <Stack gap="sm" p="md">
          {Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} height={32} radius="sm" />)}
        </Stack>
      </div>
    )
  }

  if (error) {
    return (
      <div>
        {renderToolbar}
        <Center mih={200}>
          <Stack align="center" gap="xs">
            <IconAlertCircle size={48} color="var(--mantine-color-red-6)" />
            <Title order={5}>{text.common.loadError}</Title>
            <Text c="dimmed" size="sm">{error}</Text>
            <Button onClick={onReload} mt="sm">{text.common.retry}</Button>
          </Stack>
        </Center>
      </div>
    )
  }

  if (data && data.length === 0) {
    return (
      <div>
        {renderToolbar}
        <Center mih={200}>
          <Stack align="center" gap="xs">
            <IconMoodEmpty size={48} color="var(--mantine-color-gray-5)" />
            <Text c="dimmed">{emptyText ?? text.common.empty}</Text>
          </Stack>
        </Center>
      </div>
    )
  }

  const rows = data ?? []
  const pageCount = Math.max(1, Math.ceil(rows.length / pageSize))
  const current = Math.min(page, pageCount)
  const pageRows = rows.slice((current - 1) * pageSize, current * pageSize)

  return (
    <div>
      {renderToolbar}
      <Table.ScrollContainer minWidth={480}>
        <Table striped highlightOnHover verticalSpacing="sm">
          <Table.Thead>
            <Table.Tr>
              {columns.map((col) => <Table.Th key={col.dataIndex}>{col.title}</Table.Th>)}
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {pageRows.map((record) => (
              <Table.Tr key={String(record[rowKey])}>
                {columns.map((col) => (
                  <Table.Td key={col.dataIndex}>
                    {col.render ? col.render(record[col.dataIndex], record) : String(record[col.dataIndex] ?? '')}
                  </Table.Td>
                ))}
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>

      {pageCount > 1 && (
        <Group justify="flex-end" mt="md">
          <Pagination total={pageCount} value={current} onChange={setPage} size="sm" />
        </Group>
      )}
    </div>
  )
}

// 供页面复用的工具栏标题排版：左标题、右操作区。
export function TableToolbar({ title, actions }: { title: string; actions?: ReactNode }) {
  return (
    <Group justify="space-between" align="center">
      <Title order={4} m={0}>{title}</Title>
      <Group gap="sm">{actions}</Group>
    </Group>
  )
}
