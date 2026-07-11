import { SimpleGrid, Card, Text, Title, Center, Loader, Stack, Button } from '@mantine/core'
import { IconAlertCircle } from '@tabler/icons-react'
import { api } from '../../api/endpoints'
import { useAsyncData } from '../../hooks/useAsyncData'
import { text } from '../../text'

export default function Overview() {
  const { data, loading, error, reload } = useAsyncData(async () => {
    const [users, channels, accounts] = await Promise.all([
      api.admin.listUsers(), api.admin.listChannels(), api.admin.listAccounts(),
    ])
    const models = new Set<string>()
    channels.data.filter((c) => c.enabled).forEach((c) => Object.keys(c.models ?? {}).forEach((m) => models.add(m)))
    return { users: users.data.length, channels: channels.data.length, models: models.size, accounts: accounts.data.length }
  })

  if (loading) return <Center mih={300}><Loader size="lg" /></Center>
  if (error) return (
    <Center mih={300}>
      <Stack align="center" gap="xs">
        <IconAlertCircle size={48} color="var(--mantine-color-red-6)" />
        <Text c="dimmed">{text.common.loadError}：{error}</Text>
        <Button onClick={reload} mt="sm">{text.common.retry}</Button>
      </Stack>
    </Center>
  )

  const cards = [
    { label: '用户数', value: data!.users },
    { label: '渠道数', value: data!.channels },
    { label: '启用模型数', value: data!.models },
    { label: '账户数', value: data!.accounts },
  ]
  return (
    <>
      <Title order={4} mb="md">{text.nav.overview}</Title>
      <SimpleGrid cols={{ base: 1, sm: 2, lg: 4 }}>
        {cards.map((c) => (
          <Card key={c.label}>
            <Text c="dimmed" size="sm">{c.label}</Text>
            <Text fw={700} fz={32}>{c.value}</Text>
          </Card>
        ))}
      </SimpleGrid>
    </>
  )
}
