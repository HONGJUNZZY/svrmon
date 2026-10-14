import { createApp } from 'vue'
import { createRouter, createWebHashHistory } from 'vue-router'
import App from './App.vue'
import Overview from './views/Overview.vue'
import { connect } from './store.js'
import './style.css'

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/', component: Overview },
    { path: '/s/:name', component: () => import('./views/Detail.vue'), props: true },
  ],
})

createApp(App).use(router).mount('#app')
connect()
