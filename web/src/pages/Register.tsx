import { TextInput, PasswordInput, Button, Paper, Title, Text, Stack, Anchor } from '@mantine/core'
import { useForm } from '@mantine/form'
import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/endpoints'
import { ApiError } from '../api/client'
import { text } from '../text'
import { notify } from '../notify'

export default function Register() {
  const navigate = useNavigate()
  const [loading, setLoading] = useState(false)

  const form = useForm({
    initialValues: { username: '', password: '' },
    validate: {
      username: (v) => (v.trim() ? null : '请输入用户名'),
      password: (v) => (v.length >= 8 ? null : text.register.pwTooShort),
    },
  })

  const submit = form.onSubmit(async (values) => {
    setLoading(true)
    try {
      await api.auth.register(values.username, values.password)
      notify.success(text.register.success)
      navigate('/login', { replace: true })
    } catch (e) {
      if (e instanceof ApiError && e.status === 403) notify.error('当前未开放注册')
      else notify.error(e instanceof Error ? e.message : '注册失败')
    } finally {
      setLoading(false)
    }
  })

  return (
    <div className="centered-screen">
      <Paper withBorder shadow="md" radius="md" p="xl" w={400}>
        <Title order={3} ta="center" mb="lg">{text.register.title}</Title>
        <form onSubmit={submit}>
          <Stack gap="md">
            <TextInput label={text.login.username} {...form.getInputProps('username')} />
            <PasswordInput label={text.login.password} {...form.getInputProps('password')} />
            <Button type="submit" fullWidth loading={loading}>{text.register.submit}</Button>
          </Stack>
        </form>
        {/* AUD-P0-07：注册不发额度，明确告知避免用户误以为注册即有余额。 */}
        <Text c="dimmed" size="sm" ta="center" mt="md">
          注册后账户初始额度为 0，需管理员充值后方可调用。
        </Text>
        <Text ta="center" mt="sm">
          <Anchor onClick={() => navigate('/login')}>{text.register.toLogin}</Anchor>
        </Text>
      </Paper>
    </div>
  )
}
