import { createApp } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import App from './App.vue'
import './style.css'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', component: () => import('./views/CaptureView.vue') },
    { path: '/dashboard', component: () => import('./views/DashboardView.vue') },
    { path: '/entries', component: () => import('./views/EntriesView.vue') },
    { path: '/entries/:id', component: () => import('./views/EntryDetailView.vue') },
    { path: '/search', component: () => import('./views/SearchView.vue') },
  ],
})

createApp(App).use(router).mount('#app')
