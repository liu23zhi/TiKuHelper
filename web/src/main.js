import { createApp } from 'vue'
import App from './App.vue'
import { registerConsoleUI } from './registerUI'
import router from './router/router'
import 'ant-design-vue/dist/reset.css'
import './styles/console.css'
import './styles/theme-tokens.css'
import './styles/swal-theme.css'

const app = registerConsoleUI(createApp(App))
app.use(router);
app.mount('#app')
