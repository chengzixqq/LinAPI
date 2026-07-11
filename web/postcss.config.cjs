// Mantine 官方要求的 PostCSS 配置：把 rem 混入、断点变量与颜色函数解析出来。
// postcss-preset-mantine 提供 light-dark()、rem() 等；simple-vars 提供 $mantine-breakpoint-*。
module.exports = {
  plugins: {
    'postcss-preset-mantine': {},
    'postcss-simple-vars': {
      variables: {
        'mantine-breakpoint-xs': '36em',
        'mantine-breakpoint-sm': '48em',
        'mantine-breakpoint-md': '62em',
        'mantine-breakpoint-lg': '75em',
        'mantine-breakpoint-xl': '88em',
      },
    },
  },
}
