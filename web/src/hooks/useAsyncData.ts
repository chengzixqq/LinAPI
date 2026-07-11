import { useCallback, useEffect, useState } from 'react'
import { ApiError } from '../api/client'

// useAsyncData 承载列表/详情加载的三态（loading/error/data），并给出 reload。
// 这是「三态齐全」硬指标的统一载体，页面只需据此渲染骨架/空态/错误态。
export function useAsyncData<T>(fetcher: () => Promise<T>, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [tick, setTick] = useState(0)

  const reload = useCallback(() => setTick((t) => t + 1), [])

  useEffect(() => {
    let alive = true
    setLoading(true)
    setError(null)
    fetcher()
      .then((d) => { if (alive) setData(d) })
      .catch((e) => {
        if (!alive) return
        // 401 由全局拦截处理，这里只记非鉴权错误文案。
        setError(e instanceof ApiError ? e.message : '加载失败，请重试')
      })
      .finally(() => { if (alive) setLoading(false) })
    return () => { alive = false }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tick, ...deps])

  return { data, loading, error, reload }
}
