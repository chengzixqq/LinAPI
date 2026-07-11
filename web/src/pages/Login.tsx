import { TextInput, PasswordInput, Checkbox, Button, Paper, Title, Text, Stack, Anchor } from '@mantine/core'
import { useForm } from '@mantine/form'
import { useState, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/endpoints'
import { useAuth } from '../stores/auth'
import { text } from '../text'
import { notify } from '../notify'

export default function Login() {
  const navigate = useNavigate()
  const { setMe, refresh } = useAuth()
  const [loading, setLoading] = useState(false)
  const [canRegister, setCanRegister] = useState(false)

  // 登录页据后端公开端点决定是否显示注册入口：注册未开放时不显示，避免点进去才吃 403。
  // 查询失败时保守隐藏入口（宁可少显示，也不引导用户走进一个必然失败的注册）。
  useEffect(() => {
    let alive = true
    api.auth
      .registrationStatus()
      .then((s) => { if (alive) setCanRegister(s.registration_enabled) })
      .catch(() => { if (alive) setCanRegister(false) })
    return () => { alive = false }
  }, [])

  const form = useForm({
    initialValues: { username: '', password: '', remember: false },
    validate: {
      username: (v) => (v.trim() ? null : '请输入用户名'),
      password: (v) => (v ? null : '请输入密码'),
    },
  })

  const submit = form.onSubmit(async (values) => {
    setLoading(true)
    try {
      const res = await api.auth.login(values.username, values.password, values.remember)
      // 登录成功后用 /auth/me 拿完整身份并写入 store。
      await refresh()
      setMe({ username: res.username, role: res.role as 'admin' | 'user', external_id: '' })
      navigate(res.role === 'admin' ? '/overview' : '/portal', { replace: true })
    } catch (e) {
      notify.error(e instanceof Error ? e.message : text.login.failed)
    } finally {
      setLoading(false)
    }
  })

  return (
    <div className="centered-screen">
      <Paper withBorder shadow="md" radius="md" p="xl" w={400}>
        <Title order={3} ta="center" mb="lg">{text.appName}</Title>
        <form onSubmit={submit}>
          <Stack gap="md">
            <TextInput label={text.login.username} {...form.getInputProps('username')} />
            <PasswordInput label={text.login.password} {...form.getInputProps('password')} />
            <Checkbox label={text.login.remember} {...form.getInputProps('remember', { type: 'checkbox' })} />
            <Button type="submit" fullWidth loading={loading}>{text.login.submit}</Button>
          </Stack>
        </form>
        {canRegister && (
          <Text ta="center" mt="md">
            <Anchor onClick={() => navigate('/register')}>{text.login.toRegister}</Anchor>
          </Text>
        )}
      </Paper>
    </div>
  )
}
