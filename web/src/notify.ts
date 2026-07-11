import { notifications } from '@mantine/notifications'

// notify 是 Semi Toast 的等价替代：统一成功/失败反馈入口，
// 落实四硬指标之②（反馈即时）。颜色沿用 Mantine 语义色（teal/red）。
export const notify = {
  success: (message: string) => notifications.show({ color: 'teal', message }),
  error: (message: string) => notifications.show({ color: 'red', message }),
}
