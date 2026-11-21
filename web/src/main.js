import { createApp } from 'vue'
import App from './App.vue'
import { registerPublicUI, installConsoleUIGuard } from './publicUI'
import router from './router/router'
import 'ant-design-vue/dist/reset.css'
import './styles/console.css'
import './styles/theme-tokens.css'
import './styles/swal-theme.css'

const app = registerPublicUI(createApp(App))
installConsoleUIGuard(app, router)
app.use(router);
app.mount('#app')
