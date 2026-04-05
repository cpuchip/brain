import { createApp } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import App from './App.vue'
import './style.css'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', component: () => import('./views/CaptureView.vue') },
    { path: '/dashboard', component: () => import('./views/DashboardView.vue') },
    { path: '/projects', component: () => import('./views/ProjectsView.vue') },
    { path: '/projects/:id', component: () => import('./views/ProjectDetailView.vue') },
    { path: '/entries', component: () => import('./views/EntriesView.vue') },
    { path: '/entries/:id', component: () => import('./views/EntryDetailView.vue') },
    { path: '/scheduled', component: () => import('./views/ScheduledView.vue') },
    { path: '/library', component: () => import('./views/LibraryView.vue') },
    { path: '/review', component: () => import('./views/ReviewView.vue') },
    { path: '/search', component: () => import('./views/SearchView.vue') },
  ],
})

createApp(App).use(router).mount('#app')
