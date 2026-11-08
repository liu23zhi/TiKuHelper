import { createApp } from 'vue'
import App from './App.vue'
import {
  Tabs,
  Alert,
  Button,
  Pagination,
  Table,
  Tag,
  Modal,
  Form,
  Input,
  InputNumber,
  Select,
  AutoComplete,
  Checkbox,
  Switch,
  Layout,
  Row,
  Col,
  Tooltip,
  Card,
  Space,
  Upload,
  Spin,
  Menu,
  Popconfirm,
  Badge,
  Empty,
  Radio,
  Statistic,
  ConfigProvider,
} from 'ant-design-vue'
import router from './router/router'
import 'ant-design-vue/dist/reset.css'
import './styles/console.css'
import './styles/theme-tokens.css'
import './styles/swal-theme.css'
import ConsolePageHeading from './components/ConsolePageHeading.vue'
import ConsolePanel from './components/ConsolePanel.vue'
import ConsoleSummaryGrid from './components/ConsoleSummaryGrid.vue'
import ConsoleTableShell from './components/ConsoleTableShell.vue'


const app = createApp(App)
app.use(Tabs)
app.use(Button)
app.use(Alert)
app.use(Modal)
app.use(Form)
app.use(Table)
app.use(Input)
app.use(InputNumber)
app.use(Tag)
app.use(Select)
app.use(AutoComplete)
app.use(Pagination)
app.use(Checkbox)
app.use(Switch)
app.use(Layout)
app.use(Row)
app.use(Col)
app.use(Tooltip)
app.use(Card)
app.use(Space)
app.use(Upload)
app.use(Spin)
app.use(Menu)
app.use(Popconfirm)
app.use(Badge)
app.use(Empty)
app.use(Radio)
app.use(Statistic)
app.use(ConfigProvider)
app.component('ConsolePageHeading', ConsolePageHeading)
app.component('ConsolePanel', ConsolePanel)
app.component('ConsoleSummaryGrid', ConsoleSummaryGrid)
app.component('ConsoleTableShell', ConsoleTableShell)
app.use(router);
app.mount('#app')
