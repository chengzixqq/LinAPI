import { createTheme, rem } from '@mantine/core'

// LinAPI 控制台主题。刻意与 New API / Semi 的默认蓝拉开距离：
//   - 主色用 violet（紫），品牌观感独立；
//   - 圆角整体加大（md=10px），比 Semi 默认更圆润；
//   - 默认字号/行高沿用 Mantine，中文可读性好。
// 换主色只需改 primaryColor 一处。
export const theme = createTheme({
  primaryColor: 'violet',
  defaultRadius: 'md',
  radius: {
    xs: rem(4),
    sm: rem(6),
    md: rem(10),
    lg: rem(14),
    xl: rem(20),
  },
  fontFamily:
    '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "PingFang SC", "Microsoft YaHei", sans-serif',
  headings: {
    fontWeight: '600',
  },
  components: {
    // 卡片默认带边框 + 轻阴影，弱化“方块感”。
    Card: {
      defaultProps: {
        withBorder: true,
        shadow: 'sm',
        radius: 'md',
      },
    },
  },
})
