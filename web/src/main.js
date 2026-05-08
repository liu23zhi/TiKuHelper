import { createApp } from 'vue'
import App from './App.vue'
import {
  Tabs,
  TabPane,
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
  message,
  Popconfirm,
  Badge,
  Empty,
  Radio,
  Statistic,
} from 'ant-design-vue'
import router from './router/router'
import 'ant-design-vue/dist/antd.css'


const app = createApp(App)
app.use(Tabs)
app.use(TabPane)
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
app.use(message)
app.use(router);
app.mount('#app')
