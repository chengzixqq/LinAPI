import { SimpleGrid, Card, Text, Title, Center, Loader, Stack, Button } from '@mantine/core'
import { IconAlertCircle } from '@tabler/icons-react'
import { api } from '../../api/endpoints'
import { useAsyncData } from '../../hooks/useAsyncData'
import { text } from '../../text'

export default function PortalHome() {
  const { data, loading, error, reload } = useAsyncData(() => api.me.profile())

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

  return (
    <>
      <Title order={4} mb="md">{text.nav.portalHome}</Title>
      <SimpleGrid cols={{ base: 1, sm: 2 }}>
        <Card>
          <Text c="dimmed" size="sm">账户标识</Text>
          <Text fw={600} fz={20}>{data.external_id}</Text>
        </Card>
        <Card>
          <Text c="dimmed" size="sm">当前余额</Text>
          <Text fw={700} fz={32}>{data.balance.toLocaleString()}</Text>
        </Card>
      </SimpleGrid>
    </>
  )
}
