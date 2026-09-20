import React, { useEffect, useMemo, useRef, useState } from 'react'
import { useRegisterPageRefresh } from '../../page-refresh-context'
import type { TaskCategory } from '../../task-groups'
import type { Task } from './types'
import type { APIClient } from '../../shared/api-client'
import { fetchTasks } from './api'

export function useTasks(tasks: Task[] | undefined, client: APIClient) {
  const [rows, setRows] = useState<Task[]>(tasks || [])
  const [category, setCategory] = useState<TaskCategory>('deployment')
  const [manualRefreshing, setManualRefreshing] = useState(false)
  const [backgroundRefreshing, setBackgroundRefreshing] = useState(false)
  const [lastRefreshedAt, setLastRefreshedAt] = useState<Date | null>(null)
  const [refreshFailed, setRefreshFailed] = useState(false)
  const requestInFlightRef = useRef(false)
  const mountedRef = useRef(false)
  const hasActiveTasks = useMemo(() => rows.some(task => ['pending', 'running'].includes(String(task.status || ''))), [rows])
  const hasActiveTasksRef = useRef(hasActiveTasks)
  hasActiveTasksRef.current = hasActiveTasks

  useEffect(() => { setRows(tasks || []) }, [tasks])
  useEffect(() => {
    mountedRef.current = true
    return () => { mountedRef.current = false }
  }, [])

  const loadTasks = React.useCallback(async (mode: 'manual' | 'background' = 'manual') => {
    if (mode === 'background' && requestInFlightRef.current) return
    requestInFlightRef.current = true
    if (mode === 'manual') setManualRefreshing(true)
    else setBackgroundRefreshing(true)
    try {
      const nextRows = await fetchTasks(client)
      if (!mountedRef.current) return
      setRows(nextRows)
      setLastRefreshedAt(new Date())
      setRefreshFailed(false)
    } catch (error) {
      if (mountedRef.current) setRefreshFailed(true)
      console.warn('Task refresh failed:', error)
    } finally {
      requestInFlightRef.current = false
      if (mountedRef.current) {
        if (mode === 'manual') setManualRefreshing(false)
        else setBackgroundRefreshing(false)
      }
    }
  }, [client])
  useRegisterPageRefresh(() => loadTasks('manual'))

  useEffect(() => {
    let cancelled = false
    let timer: number | undefined

    const scheduleNext = () => {
      if (cancelled || document.visibilityState !== 'visible') return
      timer = window.setTimeout(runRefresh, hasActiveTasksRef.current ? 3000 : 15000)
    }
    const runRefresh = async () => {
      if (cancelled || document.visibilityState !== 'visible') return
      await loadTasks('background')
      scheduleNext()
    }
    const handleVisibilityChange = () => {
      if (timer !== undefined) window.clearTimeout(timer)
      timer = undefined
      if (document.visibilityState === 'visible') void runRefresh()
    }

    document.addEventListener('visibilitychange', handleVisibilityChange)
    if (document.visibilityState === 'visible') void runRefresh()
    return () => {
      cancelled = true
      if (timer !== undefined) window.clearTimeout(timer)
      document.removeEventListener('visibilitychange', handleVisibilityChange)
    }
  }, [loadTasks])

  return { rows, category, setCategory, manualRefreshing, backgroundRefreshing, lastRefreshedAt, refreshFailed, hasActiveTasks }
}
