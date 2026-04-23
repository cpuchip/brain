import { computed, ref, type Ref } from 'vue'
import type { Entry } from '../api'

/**
 * Centralized rules for which entries to show by default in lists.
 *
 * Honored statuses: `someday`, `archived` are parked by default.
 * `done` rolls off after a recency window (per-view, defaults below).
 * Everything else (active, waiting, roadmap, NULL) is always visible.
 *
 * Each consumer view holds its own toggle (`showParked`) — typically
 * persisted in localStorage. When the toggle is on, we return the full
 * list. When it's off, we filter and report `hiddenCount` so the UI can
 * surface "X hidden — show all" affordances.
 */

export type ParkedStatus = 'someday' | 'archived'

export interface UseStatusFilterOptions {
  /** Days after which `done` entries roll off. 0 = always show done. Default 30. */
  doneRollOffDays?: number
  /** localStorage key to persist the showParked toggle. */
  storageKey?: string
}

export function useStatusFilter(
  entries: Ref<Entry[]>,
  options: UseStatusFilterOptions = {},
) {
  const { doneRollOffDays = 30, storageKey } = options

  const initial = storageKey ? localStorage.getItem(storageKey) === '1' : false
  const showParked = ref(initial)

  function setShowParked(v: boolean) {
    showParked.value = v
    if (storageKey) localStorage.setItem(storageKey, v ? '1' : '0')
  }

  function isParkedByStatus(e: Entry): boolean {
    return e.status === 'someday' || e.status === 'archived'
  }

  function isStaleDone(e: Entry): boolean {
    if (e.status !== 'done' || doneRollOffDays <= 0) return false
    const ts = e.updated_at || e.created_at
    if (!ts) return false
    const ageMs = Date.now() - new Date(ts).getTime()
    const ageDays = ageMs / (1000 * 60 * 60 * 24)
    return ageDays > doneRollOffDays
  }

  /** Entries to show given the current toggle. */
  const visibleEntries = computed(() => {
    if (showParked.value) return entries.value
    return entries.value.filter(e => !isParkedByStatus(e) && !isStaleDone(e))
  })

  /** Number of entries hidden by the current filter. */
  const hiddenCount = computed(() => {
    if (showParked.value) return 0
    return entries.value.length - visibleEntries.value.length
  })

  /** Breakdown of why entries are hidden — used for the toggle hover/title. */
  const hiddenBreakdown = computed(() => {
    let parked = 0
    let staleDone = 0
    for (const e of entries.value) {
      if (isParkedByStatus(e)) parked++
      else if (isStaleDone(e)) staleDone++
    }
    return { parked, staleDone, total: parked + staleDone }
  })

  return {
    showParked,
    setShowParked,
    visibleEntries,
    hiddenCount,
    hiddenBreakdown,
  }
}
