<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { RouterLink, RouterView } from 'vue-router'
import { api, type Stats } from './api'

const stats = ref<Stats | null>(null)
const reviewCount = ref(0)

async function loadCounts() {
  try {
    const [s, r] = await Promise.all([
      api.stats(),
      api.reviewQueue(),
    ])
    stats.value = s
    reviewCount.value = r.entries.length
  } catch { /* ignore */ }
}

onMounted(loadCounts)
</script>

<template>
  <div class="min-h-screen flex flex-col">
    <!-- Nav -->
    <nav class="border-b border-gray-800 bg-gray-900/80 backdrop-blur sticky top-0 z-10">
      <div class="max-w-4xl mx-auto flex items-center justify-between px-4 py-3">
        <RouterLink to="/" class="text-lg font-bold text-sky-400 hover:text-sky-300">
          🧠 Brain
        </RouterLink>
        <div class="flex items-center gap-4 text-sm">
          <RouterLink to="/" class="text-gray-400 hover:text-white" active-class="!text-white font-medium">
            Capture
          </RouterLink>
          <RouterLink to="/dashboard" class="text-gray-400 hover:text-white" active-class="!text-white font-medium">
            Dashboard
          </RouterLink>
          <RouterLink to="/entries" class="text-gray-400 hover:text-white" active-class="!text-white font-medium">
            Entries
          </RouterLink>
          <RouterLink to="/review" class="text-gray-400 hover:text-white relative" active-class="!text-white font-medium">
            Review
            <span
              v-if="reviewCount > 0"
              class="absolute -top-1.5 -right-3 bg-amber-500 text-gray-950 text-[10px] font-bold min-w-[16px] h-4 rounded-full flex items-center justify-center px-1"
            >{{ reviewCount }}</span>
          </RouterLink>
          <RouterLink to="/search" class="text-gray-400 hover:text-white" active-class="!text-white font-medium">
            Search
          </RouterLink>
          <span v-if="stats" class="text-gray-600 text-xs">{{ stats.total }} thoughts</span>
        </div>
      </div>
    </nav>

    <!-- Content -->
    <main class="flex-1 max-w-4xl w-full mx-auto px-4 py-6">
      <RouterView />
    </main>
  </div>
</template>
