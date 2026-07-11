import { AppShell, NavLink, Group, Text, Menu, Avatar, ActionIcon, UnstyledButton, useMantineColorScheme, useComputedColorScheme } from '@mantine/core'
import { IconMoon, IconSun, IconLogout, IconChevronDown } from '@tabler/icons-react'
import { Outlet, useNavigate, useLocation } from 'react-router-dom'
import { useAuth } from '../stores/auth'
import { text } from '../text'

// 按角色的导航项：to 即路由路径（去掉 basename）。
const adminNav = [
  { to: '/overview', label: text.nav.overview },
  { to: '/users', label: text.nav.users },
  { to: '/channels', label: text.nav.channels },
  { to: '/accounts', label: text.nav.accounts },
  { to: '/settings', label: text.nav.settings },
]
const userNav = [
  { to: '/portal', label: text.nav.portalHome },
  { to: '/portal/keys', label: text.nav.portalKeys },
]

// 主题切换按钮：用 Mantine color scheme 机制（自带持久化 + 跟随系统）。
function ThemeToggle() {
  const { setColorScheme } = useMantineColorScheme()
  const computed = useComputedColorScheme('light', { getInitialValueInEffect: true })
  const toggle = () => setColorScheme(computed === 'dark' ? 'light' : 'dark')
  return (
    <ActionIcon variant="default" size="lg" onClick={toggle} aria-label="切换主题">
      {computed === 'dark' ? <IconSun size={18} /> : <IconMoon size={18} />}
    </ActionIcon>
  )
}

export function AppLayout() {
  const { me, logout } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()

  const items = me?.role === 'admin' ? adminNav : userNav

  return (
    <AppShell
      header={{ height: 56 }}
      navbar={{ width: 220, breakpoint: 'sm' }}
      padding={0}
    >
      <AppShell.Header>
        <Group h="100%" px="md" justify="space-between">
          <Text fw={700} size="lg" c="violet">{text.appName}</Text>
          <Group gap="sm">
            <ThemeToggle />
            <Menu shadow="md" width={180} position="bottom-end">
              <Menu.Target>
                <UnstyledButton>
                  <Group gap={8}>
                    <Avatar color="violet" radius="xl" size={30}>
                      {me?.username?.[0]?.toUpperCase() ?? '?'}
                    </Avatar>
                    <Text size="sm">{me?.username}</Text>
                    <IconChevronDown size={14} />
                  </Group>
                </UnstyledButton>
              </Menu.Target>
              <Menu.Dropdown>
                <Menu.Item
                  color="red"
                  leftSection={<IconLogout size={16} />}
                  onClick={() => { void logout().then(() => navigate('/login')) }}
                >
                  {text.nav.logout}
                </Menu.Item>
              </Menu.Dropdown>
            </Menu>
          </Group>
        </Group>
      </AppShell.Header>

      <AppShell.Navbar p="xs">
        {items.map((item) => (
          <NavLink
            key={item.to}
            label={item.label}
            active={location.pathname === item.to}
            onClick={() => navigate(item.to)}
          />
        ))}
      </AppShell.Navbar>

      <AppShell.Main>
        <div className="page-container">
          <Outlet />
        </div>
      </AppShell.Main>
    </AppShell>
  )
}
